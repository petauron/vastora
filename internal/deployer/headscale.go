package deployer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const (
	DefaultHeadscaleImage     = "ghcr.io/juanfont/headscale:0.29.3@sha256:0e7f1c6e4ce6c2a2a001103ecd3fa645a045adf30ac8a5234fe037b43000cd72"
	DefaultCaddyImage         = gatewayruntime.CaddyImage
	DefaultHeadscaleContainer = "vastora-center-headscale"
	DefaultGatewayContainer   = gatewayruntime.CaddyContainer
	gatewayRollbackContainer  = "vastora-gateway-caddy-rollback"
)

type DockerHeadscaleInstaller struct {
	Socket                string
	ConfigDir             string
	CenterOrigin          string
	CenterDataVolume      string
	HeadscaleDataVolume   string
	HeadscaleConfigVolume string
	CaddyDataVolume       string
	CaddyConfigVolume     string
	CaddyAdminSocket      string
	HeadscaleImage        string
	CaddyImage            string
	CenterCertificatePEM  string
	CenterPrivateKeyPEM   string
	CenterAliases         []deployapi.CenterEndpointAlias
	HeadscaleAliases      []string
	HTTPClient            *http.Client
}

func (installer DockerHeadscaleInstaller) InstallHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	settings, err := installer.apiKeyRotationSettings()
	if err != nil {
		return deployapi.HeadscaleInstallResult{}, err
	}
	if !validHeadscaleOperationID(input.OperationID) {
		return deployapi.HeadscaleInstallResult{}, errors.New("deployer: a valid Headscale install operation ID is required")
	}
	encodedRequest, err := json.Marshal(input)
	if err != nil {
		return deployapi.HeadscaleInstallResult{}, err
	}
	requestHash := fmt.Sprintf("%x", sha256.Sum256(encodedRequest))
	pendingPath := filepath.Join(settings.ConfigDir, "headscale-install.json")
	if pending, found, err := loadPendingHeadscaleInstall(pendingPath); err != nil {
		return deployapi.HeadscaleInstallResult{}, err
	} else if found {
		if pending.OperationID != input.OperationID || pending.RequestHash != requestHash {
			return deployapi.HeadscaleInstallResult{}, errors.New("deployer: another Headscale installation is awaiting Center commit")
		}
		docker, err := client.New(client.WithHost(settings.Socket))
		if err != nil {
			return deployapi.HeadscaleInstallResult{}, fmt.Errorf("deployer: connect Docker: %w", err)
		}
		defer docker.Close()
		if managed, err := inspectManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale"); err != nil {
			return deployapi.HeadscaleInstallResult{}, err
		} else if managed == nil {
			return deployapi.HeadscaleInstallResult{}, errors.New("deployer: pending Headscale installation lost its managed container")
		}
		records, err := listHeadscaleAPIKeys(ctx, docker, DefaultHeadscaleContainer)
		if err != nil {
			return deployapi.HeadscaleInstallResult{}, err
		}
		record, found, err := findHeadscaleAPIKeyRecord(records, pending.Result.APIKeyPrefix)
		if err != nil {
			return deployapi.HeadscaleInstallResult{}, err
		}
		if found && record.APIKeyID == pending.Result.APIKeyID && record.APIKeyExpiresAt.After(time.Now()) {
			pending.Result.APIKeyExpiresAt = record.APIKeyExpiresAt
			return pending.Result, nil
		}
		if found && record.APIKeyID != pending.Result.APIKeyID {
			return deployapi.HeadscaleInstallResult{}, errors.New("deployer: pending Headscale installation key identity changed")
		}
		if found {
			if _, err := runHeadscaleCommand(ctx, docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", pending.Result.APIKeyPrefix); err != nil {
				return deployapi.HeadscaleInstallResult{}, err
			}
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deployapi.HeadscaleInstallResult{}, fmt.Errorf("deployer: clear unusable pending Headscale installation: %w", err)
		}
	}
	endpoint, apiKey, err := installer.applyHeadscale(ctx, input, true)
	if err != nil {
		return deployapi.HeadscaleInstallResult{}, err
	}
	result := deployapi.HeadscaleInstallResult{
		Endpoint: endpoint, APIKey: apiKey.APIKey, APIKeyID: apiKey.APIKeyID,
		APIKeyPrefix: apiKey.APIKeyPrefix, APIKeyExpiresAt: apiKey.APIKeyExpiresAt,
	}
	encodedPending, err := json.Marshal(pendingHeadscaleInstall{OperationID: input.OperationID, RequestHash: requestHash, Result: result})
	if err == nil {
		err = writeAtomic(pendingPath, encodedPending, 0o600)
	}
	if err != nil {
		docker, connectErr := client.New(client.WithHost(settings.Socket))
		if connectErr != nil {
			return deployapi.HeadscaleInstallResult{}, errors.Join(fmt.Errorf("deployer: persist pending Headscale installation: %w", err), connectErr)
		}
		defer docker.Close()
		_, cleanupErr := runHeadscaleCommand(context.WithoutCancel(ctx), docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", result.APIKeyPrefix)
		return deployapi.HeadscaleInstallResult{}, errors.Join(fmt.Errorf("deployer: persist pending Headscale installation: %w", err), cleanupErr)
	}
	return result, nil
}

func (installer DockerHeadscaleInstaller) CommitHeadscaleInstall(_ context.Context, input deployapi.HeadscaleInstallCommitRequest) error {
	settings, err := installer.apiKeyRotationSettings()
	if err != nil {
		return err
	}
	if !validHeadscaleOperationID(input.OperationID) {
		return errors.New("deployer: a valid Headscale install operation ID is required")
	}
	pendingPath := filepath.Join(settings.ConfigDir, "headscale-install.json")
	pending, found, err := loadPendingHeadscaleInstall(pendingPath)
	if err != nil || !found {
		return err
	}
	if pending.OperationID != input.OperationID {
		return errors.New("deployer: pending Headscale installation does not match the commit")
	}
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deployer: commit pending Headscale installation: %w", err)
	}
	return nil
}

func (installer DockerHeadscaleInstaller) ReconcileHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest) error {
	_, _, err := installer.applyHeadscale(ctx, input, false)
	return err
}

func (installer DockerHeadscaleInstaller) applyHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest, createAPIKey bool) (string, deployapi.HeadscaleAPIKeyRotation, error) {
	input.DNSPolicy, input.DNSResolvers, err := deployapi.NormalizeHeadscaleDNS(input.DNSPolicy, input.DNSResolvers)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: Headscale DNS: %w", err)
	}
	settings, centerURL, headscaleURL, err := installer.settings(input)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	publicEndpoints := append([]string{headscaleURL}, settings.HeadscaleAliases...)
	bindAddresses, err := gatewayBindAddresses(ctx, input.PublicAddress, input.GatewayBindAddress, publicEndpoints...)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	centerBindAddresses, err := centerPrivateBindAddresses(input.CenterPrivateBindAddress)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	docker, err := client.New(client.WithHost(settings.Socket))
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: connect Docker: %w", err)
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	headscaleConfig, err := renderHeadscaleConfig(headscaleURL, input.DNSPolicy, input.DNSResolvers)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	headscaleFiles := map[string][]byte{
		"config.yaml":   headscaleConfig,
		"derp.yaml":     renderHeadscaleDERPMap(),
		"policy.hujson": renderHeadscalePolicy(),
	}
	for _, image := range []string{settings.HeadscaleImage, settings.CaddyImage} {
		pull, err := docker.ImagePull(ctx, image, client.ImagePullOptions{})
		if err != nil {
			return "", deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: pull fixed infrastructure image: %w", err)
		}
		_, _ = io.Copy(io.Discard, pull)
		_ = pull.Close()
	}
	for _, name := range []string{settings.HeadscaleDataVolume, settings.HeadscaleConfigVolume, settings.CaddyDataVolume, settings.CaddyConfigVolume} {
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: map[string]string{
			gatewayruntime.ManagedLabel:   "true",
			gatewayruntime.ComponentLabel: "center-headscale-storage",
		}}); err != nil {
			return "", deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: create volume %s: %w", name, err)
		}
	}
	headscaleReplacement, err := settings.replaceHeadscale(ctx, docker, headscaleFiles)
	if err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	if err := waitForURL(ctx, settings.HTTPClient, "http://"+DefaultHeadscaleContainer+":8081/health", 90*time.Second); err != nil {
		rollbackErr := headscaleReplacement.rollback(ctx, docker)
		return "", deployapi.HeadscaleAPIKeyRotation{}, errors.Join(fmt.Errorf("deployer: Headscale did not become healthy: %w", err), rollbackErr)
	}
	apiKey := deployapi.HeadscaleAPIKeyRotation{}
	keepAPIKey := false
	if createAPIKey {
		apiKey, err = createHeadscaleAPIKey(ctx, docker, DefaultHeadscaleContainer)
		if err != nil {
			rollbackErr := headscaleReplacement.rollback(ctx, docker)
			return "", deployapi.HeadscaleAPIKeyRotation{}, errors.Join(err, rollbackErr)
		}
		defer func() {
			if !keepAPIKey {
				_, _ = runHeadscaleCommand(context.WithoutCancel(ctx), docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", apiKey.APIKeyPrefix)
			}
		}()
	}
	replacement, err := settings.replaceGateway(ctx, docker, renderCaddyfile(centerURL, settings.CenterOrigin, headscaleURL, centerBindAddresses, bindAddresses, settings.CenterAliases, settings.HeadscaleAliases), centerBindAddresses, bindAddresses)
	if err != nil {
		rollbackErr := headscaleReplacement.rollback(ctx, docker)
		return "", deployapi.HeadscaleAPIKeyRotation{}, errors.Join(err, rollbackErr)
	}
	for _, health := range []struct {
		endpoint string
		path     string
		port     int
	}{{centerURL, "/healthz", 12443}, {headscaleURL, "/health", 443}} {
		if err := waitForLocalGateway(ctx, health.endpoint, health.path, health.port, 3*time.Minute); err != nil {
			rollbackErr := errors.Join(replacement.rollback(ctx, docker), headscaleReplacement.rollback(ctx, docker))
			return "", deployapi.HeadscaleAPIKeyRotation{}, errors.Join(fmt.Errorf("deployer: HTTPS gateway did not make %s%s healthy: %w", health.endpoint, health.path, err), rollbackErr)
		}
	}
	if err := replacement.commit(ctx, docker); err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	if err := headscaleReplacement.commit(ctx, docker); err != nil {
		return "", deployapi.HeadscaleAPIKeyRotation{}, err
	}
	keepAPIKey = true
	return headscaleURL, apiKey, nil
}

func (installer DockerHeadscaleInstaller) settings(input deployapi.HeadscaleInstallRequest) (DockerHeadscaleInstaller, string, string, error) {
	centerURL, err := normalizePublicURL(input.CenterURL)
	if err != nil {
		return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Center URL: %w", err)
	}
	headscaleURL, err := normalizePublicURL(input.HeadscaleURL)
	if err != nil {
		return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Headscale URL: %w", err)
	}
	if centerURL == headscaleURL {
		return DockerHeadscaleInstaller{}, "", "", errors.New("deployer: Center and Headscale require different hostnames")
	}
	if err := validateCenterCertificate(centerURL, input.CenterCertificatePEM, input.CenterCertificateKeyPEM); err != nil {
		return DockerHeadscaleInstaller{}, "", "", err
	}
	seenCenter := map[string]struct{}{centerURL: {}}
	centerAliases := make([]deployapi.CenterEndpointAlias, 0, len(input.CenterAliases))
	for _, alias := range input.CenterAliases {
		aliasURL, normalizeErr := normalizePublicURL(alias.URL)
		if normalizeErr != nil {
			return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Center alias URL: %w", normalizeErr)
		}
		if _, exists := seenCenter[aliasURL]; exists {
			continue
		}
		if certificateErr := validateCenterCertificate(aliasURL, alias.CertificatePEM, alias.CertificateKeyPEM); certificateErr != nil {
			return DockerHeadscaleInstaller{}, "", "", certificateErr
		}
		seenCenter[aliasURL] = struct{}{}
		alias.URL = aliasURL
		centerAliases = append(centerAliases, alias)
	}
	seenHeadscale := map[string]struct{}{headscaleURL: {}}
	headscaleAliases := make([]string, 0, len(input.HeadscaleAliases))
	for _, alias := range input.HeadscaleAliases {
		aliasURL, normalizeErr := normalizePublicURL(alias)
		if normalizeErr != nil {
			return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Headscale alias URL: %w", normalizeErr)
		}
		if _, exists := seenHeadscale[aliasURL]; exists {
			continue
		}
		seenHeadscale[aliasURL] = struct{}{}
		headscaleAliases = append(headscaleAliases, aliasURL)
	}
	installer.CenterCertificatePEM = input.CenterCertificatePEM
	installer.CenterPrivateKeyPEM = input.CenterCertificateKeyPEM
	installer.CenterAliases = centerAliases
	installer.HeadscaleAliases = headscaleAliases
	if installer.Socket == "" {
		installer.Socket = "unix:///var/run/docker.sock"
	}
	if installer.ConfigDir == "" || !filepath.IsAbs(installer.ConfigDir) {
		return DockerHeadscaleInstaller{}, "", "", errors.New("deployer: absolute Headscale config directory is required")
	}
	if installer.CenterOrigin == "" {
		installer.CenterOrigin = dockerruntime.CenterAlias + ":8080"
	}
	if installer.CenterDataVolume == "" {
		installer.CenterDataVolume = "vastora_center-data"
	}
	if installer.HeadscaleDataVolume == "" {
		installer.HeadscaleDataVolume = "vastora_headscale-data"
	}
	if installer.HeadscaleConfigVolume == "" {
		installer.HeadscaleConfigVolume = "vastora_headscale-config"
	}
	if installer.CaddyDataVolume == "" {
		installer.CaddyDataVolume = "vastora_headscale-caddy-data"
	}
	if installer.CaddyConfigVolume == "" {
		installer.CaddyConfigVolume = "vastora_headscale-caddy-config"
	}
	if installer.CaddyAdminSocket == "" {
		installer.CaddyAdminSocket = gatewayruntime.CaddyAdminSocket
	}
	if installer.HeadscaleImage == "" {
		installer.HeadscaleImage = DefaultHeadscaleImage
	}
	if installer.CaddyImage == "" {
		installer.CaddyImage = DefaultCaddyImage
	}
	if installer.HTTPClient == nil {
		installer.HTTPClient = &http.Client{Timeout: 8 * time.Second}
	}
	return installer, centerURL, headscaleURL, nil
}

func validateCenterCertificate(endpoint, certificatePEM, privateKeyPEM string) error {
	certificatePair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(privateKeyPEM))
	if err != nil || len(certificatePair.Certificate) == 0 {
		return errors.New("deployer: a valid Center HTTPS certificate and key are required")
	}
	leaf, err := x509.ParseCertificate(certificatePair.Certificate[0])
	if err != nil || leaf.VerifyHostname(strings.TrimPrefix(endpoint, "https://")) != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return errors.New("deployer: Center HTTPS certificate is invalid for its private hostname")
	}
	return nil
}

func (installer DockerHeadscaleInstaller) headscaleContainerConfig() (*container.Config, *container.HostConfig) {
	stunPort := dockernetwork.MustParsePort("3478/udp")
	return &container.Config{
			Image:        installer.HeadscaleImage,
			Cmd:          []string{"serve"},
			Labels:       map[string]string{"io.vastora.managed": "true", "io.vastora.component": "center-headscale"},
			ExposedPorts: dockernetwork.PortSet{dockernetwork.MustParsePort("8081/tcp"): struct{}{}, stunPort: struct{}{}},
		}, &container.HostConfig{
			NetworkMode:    container.NetworkMode(dockerruntime.NetworkName),
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/var/run/headscale": "rw,noexec,nosuid,size=16m,mode=1777"},
			PortBindings: dockernetwork.PortMap{
				stunPort: []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: "3478"}},
			},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: installer.HeadscaleDataVolume, Target: "/var/lib/headscale"},
				{Type: mount.TypeVolume, Source: installer.HeadscaleConfigVolume, Target: "/etc/headscale", ReadOnly: true},
				{Type: mount.TypeVolume, Source: installer.CenterDataVolume, Target: "/var/lib/vastora-shared", ReadOnly: true},
			},
		}
}

func inspectManagedContainer(ctx context.Context, docker *client.Client, name, component string) (*client.ContainerInspectResult, error) {
	inspected, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deployer: inspect managed container %s: %w", name, err)
	}
	if inspected.Container.Config == nil {
		return nil, fmt.Errorf("deployer: managed container %s has no Docker configuration", name)
	}
	labels := inspected.Container.Config.Labels
	if labels[gatewayruntime.ManagedLabel] != "true" || labels[gatewayruntime.ComponentLabel] != component {
		return nil, fmt.Errorf("deployer: container name %s is already used by an unmanaged workload", name)
	}
	return &inspected, nil
}

type headscaleAPIKeyRecord struct {
	ID         json.RawMessage `json:"id"`
	Prefix     string          `json:"prefix"`
	Expiration json.RawMessage `json:"expiration"`
}

type pendingHeadscaleAPIKeyRotation struct {
	PreviousPrefix string                            `json:"previousPrefix"`
	Rotation       deployapi.HeadscaleAPIKeyRotation `json:"rotation"`
}

type pendingHeadscaleInstall struct {
	OperationID string                           `json:"operationId"`
	RequestHash string                           `json:"requestHash"`
	Result      deployapi.HeadscaleInstallResult `json:"result"`
}

func createHeadscaleAPIKey(ctx context.Context, docker *client.Client, containerName string) (deployapi.HeadscaleAPIKeyRotation, error) {
	key, err := runHeadscaleCommand(ctx, docker, containerName, "headscale", "apikeys", "create", "--expiration", "365d")
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	}
	key = strings.TrimSpace(key)
	prefix, err := headscaleAPIKeyPrefix(key)
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	}
	cleanup := func(cause error) error {
		_, cleanupErr := runHeadscaleCommand(context.WithoutCancel(ctx), docker, containerName, "headscale", "apikeys", "delete", "--prefix", prefix)
		return errors.Join(cause, cleanupErr)
	}
	records, err := listHeadscaleAPIKeys(ctx, docker, containerName)
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, cleanup(err)
	}
	for _, record := range records {
		recordPrefix, err := headscaleAPIKeyPrefix(record.Prefix)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, cleanup(err)
		}
		if recordPrefix != prefix {
			continue
		}
		id, err := parseHeadscaleAPIKeyID(record.ID)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, cleanup(err)
		}
		expiresAt, err := parseHeadscaleAPIKeyExpiration(record.Expiration)
		if err != nil || !expiresAt.After(time.Now()) {
			return deployapi.HeadscaleAPIKeyRotation{}, cleanup(errors.New("deployer: Headscale returned an invalid API key expiry"))
		}
		return deployapi.HeadscaleAPIKeyRotation{APIKey: key, APIKeyID: id, APIKeyPrefix: prefix, APIKeyExpiresAt: expiresAt}, nil
	}
	return deployapi.HeadscaleAPIKeyRotation{}, cleanup(errors.New("deployer: created Headscale API key was not present in the authoritative key list"))
}

func (installer DockerHeadscaleInstaller) PrepareHeadscaleAPIKeyRotation(ctx context.Context, input deployapi.HeadscaleAPIKeyRotationRequest) (deployapi.HeadscaleAPIKeyRotation, error) {
	settings, err := installer.apiKeyRotationSettings()
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	}
	input.CurrentPrefix = strings.TrimSpace(input.CurrentPrefix)
	if input.CurrentPrefix == "" {
		return deployapi.HeadscaleAPIKeyRotation{}, errors.New("deployer: current Headscale API key prefix is required")
	}
	docker, err := client.New(client.WithHost(settings.Socket))
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: connect Docker: %w", err)
	}
	defer docker.Close()
	if managed, err := inspectManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale"); err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	} else if managed == nil {
		return deployapi.HeadscaleAPIKeyRotation{}, errors.New("deployer: managed Headscale container is not installed")
	}
	pendingPath := filepath.Join(settings.ConfigDir, "api-key-rotation.json")
	if pending, found, err := loadPendingHeadscaleAPIKeyRotation(pendingPath); err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	} else if found {
		if pending.PreviousPrefix != input.CurrentPrefix {
			return deployapi.HeadscaleAPIKeyRotation{}, errors.New("deployer: another Headscale API key rotation is pending")
		}
		records, err := listHeadscaleAPIKeys(ctx, docker, DefaultHeadscaleContainer)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, err
		}
		record, found, err := findHeadscaleAPIKeyRecord(records, pending.Rotation.APIKeyPrefix)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, err
		}
		if found && record.APIKeyID == pending.Rotation.APIKeyID && record.APIKeyExpiresAt.After(time.Now()) {
			pending.Rotation.APIKeyExpiresAt = record.APIKeyExpiresAt
			return pending.Rotation, nil
		}
		if found && record.APIKeyID != pending.Rotation.APIKeyID {
			return deployapi.HeadscaleAPIKeyRotation{}, errors.New("deployer: pending Headscale API key identity no longer matches the authoritative key list")
		}
		if found {
			if _, err := runHeadscaleCommand(ctx, docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", pending.Rotation.APIKeyPrefix); err != nil {
				return deployapi.HeadscaleAPIKeyRotation{}, err
			}
		}
		if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return deployapi.HeadscaleAPIKeyRotation{}, fmt.Errorf("deployer: clear unusable pending Headscale API key rotation: %w", err)
		}
	}
	rotation, err := createHeadscaleAPIKey(ctx, docker, DefaultHeadscaleContainer)
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	}
	encoded, err := json.Marshal(pendingHeadscaleAPIKeyRotation{PreviousPrefix: input.CurrentPrefix, Rotation: rotation})
	if err != nil {
		return deployapi.HeadscaleAPIKeyRotation{}, err
	}
	if err := writeAtomic(pendingPath, encoded, 0o600); err != nil {
		_, cleanupErr := runHeadscaleCommand(context.WithoutCancel(ctx), docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", rotation.APIKeyPrefix)
		return deployapi.HeadscaleAPIKeyRotation{}, errors.Join(fmt.Errorf("deployer: persist pending Headscale API key rotation: %w", err), cleanupErr)
	}
	return rotation, nil
}

func (installer DockerHeadscaleInstaller) CommitHeadscaleAPIKeyRotation(ctx context.Context, input deployapi.HeadscaleAPIKeyCommitRequest) error {
	settings, err := installer.apiKeyRotationSettings()
	if err != nil {
		return err
	}
	input.PreviousPrefix = strings.TrimSpace(input.PreviousPrefix)
	input.CurrentPrefix = strings.TrimSpace(input.CurrentPrefix)
	if input.PreviousPrefix == "" || input.CurrentPrefix == "" || input.PreviousPrefix == input.CurrentPrefix {
		return errors.New("deployer: distinct previous and current Headscale API key prefixes are required")
	}
	pendingPath := filepath.Join(settings.ConfigDir, "api-key-rotation.json")
	if pending, found, err := loadPendingHeadscaleAPIKeyRotation(pendingPath); err != nil {
		return err
	} else if found && (pending.PreviousPrefix != input.PreviousPrefix || pending.Rotation.APIKeyPrefix != input.CurrentPrefix) {
		return errors.New("deployer: pending Headscale API key rotation does not match the commit")
	}
	docker, err := client.New(client.WithHost(settings.Socket))
	if err != nil {
		return fmt.Errorf("deployer: connect Docker: %w", err)
	}
	defer docker.Close()
	records, err := listHeadscaleAPIKeys(ctx, docker, DefaultHeadscaleContainer)
	if err != nil {
		return err
	}
	previousFound, err := validateHeadscaleAPIKeyRotationCommit(records, input.PreviousPrefix, input.CurrentPrefix, time.Now())
	if err != nil {
		return err
	}
	if previousFound {
		if _, err := runHeadscaleCommand(ctx, docker, DefaultHeadscaleContainer, "headscale", "apikeys", "delete", "--prefix", input.PreviousPrefix); err != nil {
			return err
		}
	}
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("deployer: clear pending Headscale API key rotation: %w", err)
	}
	return nil
}

func validateHeadscaleAPIKeyRotationCommit(records []headscaleAPIKeyRecord, previousPrefix, currentPrefix string, now time.Time) (bool, error) {
	currentFound := false
	previousFound := false
	for _, record := range records {
		recordPrefix, err := headscaleAPIKeyPrefix(record.Prefix)
		if err != nil {
			return false, err
		}
		if recordPrefix == currentPrefix {
			expiresAt, parseErr := parseHeadscaleAPIKeyExpiration(record.Expiration)
			if parseErr != nil || !expiresAt.After(now) {
				return false, errors.New("deployer: replacement Headscale API key is expired")
			}
			currentFound = true
		}
		previousFound = previousFound || recordPrefix == previousPrefix
	}
	if !currentFound {
		return false, errors.New("deployer: replacement Headscale API key is not present")
	}
	return previousFound, nil
}

func (installer DockerHeadscaleInstaller) apiKeyRotationSettings() (DockerHeadscaleInstaller, error) {
	if installer.Socket == "" {
		installer.Socket = "unix:///var/run/docker.sock"
	}
	if installer.ConfigDir == "" || !filepath.IsAbs(installer.ConfigDir) {
		return DockerHeadscaleInstaller{}, errors.New("deployer: absolute Headscale config directory is required")
	}
	return installer, nil
}

func loadPendingHeadscaleAPIKeyRotation(path string) (pendingHeadscaleAPIKeyRotation, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pendingHeadscaleAPIKeyRotation{}, false, nil
	}
	if err != nil {
		return pendingHeadscaleAPIKeyRotation{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 {
		return pendingHeadscaleAPIKeyRotation{}, false, errors.New("deployer: pending Headscale API key rotation file is not a protected regular file")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return pendingHeadscaleAPIKeyRotation{}, false, err
	}
	var pending pendingHeadscaleAPIKeyRotation
	if json.Unmarshal(encoded, &pending) != nil || pending.PreviousPrefix == "" || len(pending.Rotation.APIKey) < 20 || pending.Rotation.APIKeyID == 0 || pending.Rotation.APIKeyPrefix == "" || pending.Rotation.APIKeyExpiresAt.IsZero() {
		return pendingHeadscaleAPIKeyRotation{}, false, errors.New("deployer: pending Headscale API key rotation file is invalid")
	}
	return pending, true, nil
}

func loadPendingHeadscaleInstall(path string) (pendingHeadscaleInstall, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pendingHeadscaleInstall{}, false, nil
	}
	if err != nil {
		return pendingHeadscaleInstall{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 {
		return pendingHeadscaleInstall{}, false, errors.New("deployer: pending Headscale installation file is not a protected regular file")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return pendingHeadscaleInstall{}, false, err
	}
	var pending pendingHeadscaleInstall
	if json.Unmarshal(encoded, &pending) != nil || !validHeadscaleOperationID(pending.OperationID) || len(pending.RequestHash) != sha256.Size*2 || pending.Result.Endpoint == "" || len(pending.Result.APIKey) < 20 || pending.Result.APIKeyID == 0 || pending.Result.APIKeyPrefix == "" || pending.Result.APIKeyExpiresAt.IsZero() {
		return pendingHeadscaleInstall{}, false, errors.New("deployer: pending Headscale installation file is invalid")
	}
	return pending, true, nil
}

func validHeadscaleOperationID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func listHeadscaleAPIKeys(ctx context.Context, docker *client.Client, containerName string) ([]headscaleAPIKeyRecord, error) {
	output, err := runHeadscaleCommand(ctx, docker, containerName, "headscale", "apikeys", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	var records []headscaleAPIKeyRecord
	if err := json.Unmarshal([]byte(output), &records); err != nil {
		return nil, fmt.Errorf("deployer: decode Headscale API key list: %w", err)
	}
	return records, nil
}

func headscaleAPIKeyPrefix(key string) (string, error) {
	const marker = "hskey-api-"
	if strings.HasPrefix(key, marker) && len(key) > len(marker)+12 && key[len(marker)+12] == '-' {
		return key[len(marker) : len(marker)+12], nil
	}
	if prefix, _, found := strings.Cut(key, "."); found && len(prefix) == 7 {
		return prefix, nil
	}
	return "", errors.New("deployer: Headscale returned an invalid API key")
}

func parseHeadscaleAPIKeyID(encoded json.RawMessage) (uint64, error) {
	value := strings.Trim(string(encoded), `"`)
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("deployer: Headscale returned an invalid API key ID")
	}
	return id, nil
}

func parseHeadscaleAPIKeyExpiration(encoded json.RawMessage) (time.Time, error) {
	var timestamp string
	if err := json.Unmarshal(encoded, &timestamp); err == nil {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, timestamp)
		if parseErr != nil {
			return time.Time{}, errors.New("deployer: Headscale returned an invalid API key expiry")
		}
		return expiresAt.UTC(), nil
	}
	var protobufTimestamp struct {
		Seconds json.RawMessage `json:"seconds"`
		Nanos   int64           `json:"nanos"`
	}
	if err := json.Unmarshal(encoded, &protobufTimestamp); err != nil || len(protobufTimestamp.Seconds) == 0 || protobufTimestamp.Nanos < 0 || protobufTimestamp.Nanos >= int64(time.Second) {
		return time.Time{}, errors.New("deployer: Headscale returned an invalid API key expiry")
	}
	seconds, err := strconv.ParseInt(strings.Trim(string(protobufTimestamp.Seconds), `"`), 10, 64)
	if err != nil {
		return time.Time{}, errors.New("deployer: Headscale returned an invalid API key expiry")
	}
	return time.Unix(seconds, protobufTimestamp.Nanos).UTC(), nil
}

func findHeadscaleAPIKeyRecord(records []headscaleAPIKeyRecord, prefix string) (deployapi.HeadscaleAPIKeyRotation, bool, error) {
	for _, record := range records {
		recordPrefix, err := headscaleAPIKeyPrefix(record.Prefix)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, false, err
		}
		if recordPrefix != prefix {
			continue
		}
		id, err := parseHeadscaleAPIKeyID(record.ID)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, false, err
		}
		expiresAt, err := parseHeadscaleAPIKeyExpiration(record.Expiration)
		if err != nil {
			return deployapi.HeadscaleAPIKeyRotation{}, false, err
		}
		return deployapi.HeadscaleAPIKeyRotation{APIKeyID: id, APIKeyPrefix: recordPrefix, APIKeyExpiresAt: expiresAt}, true, nil
	}
	return deployapi.HeadscaleAPIKeyRotation{}, false, nil
}

func runHeadscaleCommand(ctx context.Context, docker *client.Client, containerName string, command ...string) (string, error) {
	exec, err := docker.ExecCreate(ctx, containerName, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          command,
	})
	if err != nil {
		return "", fmt.Errorf("deployer: create Headscale API key command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("deployer: run Headscale API key command: %w", err)
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return "", fmt.Errorf("deployer: read Headscale API key command: %w", err)
	}
	status, err := docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("deployer: inspect Headscale API key command: %w", err)
	}
	if status.ExitCode != 0 {
		return "", fmt.Errorf("deployer: Headscale API key command failed: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func copyFile(ctx context.Context, docker *client.Client, containerID, destination, name string, content []byte) error {
	return copyFileMode(ctx, docker, containerID, destination, name, content, 0o600)
}

func copyFileMode(ctx context.Context, docker *client.Client, containerID, destination, name string, content []byte, mode int64) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if directory := filepath.Dir(name); directory != "." {
		if err := writer.WriteHeader(&tar.Header{Name: directory, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
	}
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	if _, err := writer.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: destination, Content: bytes.NewReader(archive.Bytes())}); err != nil {
		return fmt.Errorf("deployer: install gateway configuration: %w", err)
	}
	return nil
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("deployer: create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-config-*")
	if err != nil {
		return fmt.Errorf("deployer: create temporary configuration: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("deployer: publish configuration: %w", err)
	}
	return nil
}

func waitForURL(ctx context.Context, httpClient *http.Client, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}

func waitForLocalGateway(ctx context.Context, endpoint, healthPath string, internalPort int, timeout time.Duration) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(dockerruntime.CaddyAlias, strconv.Itoa(internalPort)))
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+healthPath, nil)
		if err != nil {
			return err
		}
		request.Host = parsed.Host
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}
