package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type SystemDomainView struct {
	Namespace                string                    `json:"namespace"`
	CenterURL                string                    `json:"centerUrl"`
	HeadscaleURL             string                    `json:"headscaleUrl"`
	CloudflareZone           string                    `json:"cloudflareZone"`
	Aliases                  []SystemEndpointAliasView `json:"aliases"`
	ActivePublications       int                       `json:"activePublications"`
	PendingCleanup           int                       `json:"pendingCleanup"`
	BuiltinHeadscale         bool                      `json:"builtinHeadscale"`
	CloudflareOAuthAvailable bool                      `json:"cloudflareOAuthAvailable"`
}

type SystemDomainSwitchInput struct {
	ZoneID  string `json:"zoneId"`
	Confirm bool   `json:"confirm"`
}

type SystemDomainSwitchResult struct {
	SystemDomainView
	PreviousCenterURL    string `json:"previousCenterUrl"`
	PreviousHeadscaleURL string `json:"previousHeadscaleUrl"`
	BackupCreated        bool   `json:"backupCreated"`
}

func (s *Store) SystemDomain(ctx context.Context) (SystemDomainView, error) {
	network, err := s.CenterNetworkConfig(ctx)
	if err != nil {
		return SystemDomainView{}, err
	}
	var headscaleURL, headscaleMode, cloudflareZone string
	if err := s.db.QueryRowContext(ctx, `SELECT mode, endpoint FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&headscaleMode, &headscaleURL); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SystemDomainView{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&cloudflareZone); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SystemDomainView{}, err
	}
	aliases, err := s.ListSystemEndpointAliases(ctx)
	if err != nil {
		return SystemDomainView{}, err
	}
	var active, cleanup int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE status <> 'stopped'`).Scan(&active); err != nil {
		return SystemDomainView{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE cleanup_pending = 1`).Scan(&cleanup); err != nil {
		return SystemDomainView{}, err
	}
	namespace := domainNamespaceFromCenterURL(network.AgentConnectURL)
	return SystemDomainView{
		Namespace: namespace, CenterURL: network.AgentConnectURL, HeadscaleURL: headscaleURL,
		CloudflareZone: cloudflareZone, Aliases: aliases, ActivePublications: active,
		PendingCleanup: cleanup, BuiltinHeadscale: headscaleMode == "builtin",
		CloudflareOAuthAvailable: s.CloudflareOAuthAvailable(),
	}, nil
}

func domainNamespaceFromCenterURL(endpoint string) string {
	hostname, err := gatewayEndpointHostname(endpoint)
	if err != nil || !strings.HasPrefix(hostname, "center.") {
		return ""
	}
	return strings.TrimPrefix(hostname, "center.")
}

func systemNamespaceForZone(zoneName string) string {
	return "vastora." + strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
}

func (s *Server) SwitchSystemDomain(ctx context.Context, input SystemDomainSwitchInput) (SystemDomainSwitchResult, error) {
	if !input.Confirm {
		return SystemDomainSwitchResult{}, errors.New("center: confirm the system domain switch")
	}
	if s.infrastructure == nil {
		return SystemDomainSwitchResult{}, errors.New("center: this installation does not include the deployment helper")
	}
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()

	current, err := s.store.SystemDomain(ctx)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	if !current.BuiltinHeadscale {
		return SystemDomainSwitchResult{}, errors.New("center: switching the system domain currently requires bundled Headscale")
	}
	if current.ActivePublications != 0 || current.PendingCleanup != 0 {
		return SystemDomainSwitchResult{}, errors.New("center: stop all access points and wait for cleanup before switching the system domain")
	}
	cloudflare, selected, err := s.store.cloudflareForZone(ctx, input.ZoneID)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	zoneName, err := cloudflare.verify(ctx)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	namespace := systemNamespaceForZone(zoneName)
	centerURL := "https://center." + namespace
	headscaleURL := "https://headscale." + namespace
	if centerURL == current.CenterURL && headscaleURL == current.HeadscaleURL {
		return SystemDomainSwitchResult{}, errors.New("center: the selected domain is already in use")
	}
	binding, configured, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	if !configured || net.ParseIP(binding.PublicAddress) == nil {
		return SystemDomainSwitchResult{}, errors.New("center: the public gateway address is not configured")
	}
	centerPrivateBindAddress, err := s.store.coLocatedHeadscaleAddress(ctx)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	} else if centerPrivateBindAddress == "" {
		return SystemDomainSwitchResult{}, errors.New("center: the co-located Agent must join the private network before switching domains")
	}
	if _, err := s.store.createSystemDomainBackup(ctx); err != nil {
		return SystemDomainSwitchResult{}, err
	}

	newDNS, createdDNS, err := ensureSystemDNSRecord(ctx, cloudflare, headscaleURL, binding.PublicAddress)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	rollbackDNS := func() {
		if createdDNS {
			_ = cloudflare.deleteDNSRecord(context.WithoutCancel(ctx), newDNS.ID)
		}
	}
	newCertificate, err := s.store.issueDomainCertificate(ctx, cloudflare, zoneName, strings.TrimPrefix(centerURL, "https://"))
	if err != nil {
		rollbackDNS()
		return SystemDomainSwitchResult{}, err
	}
	oldHostname, err := gatewayEndpointHostname(current.CenterURL)
	if err != nil {
		rollbackDNS()
		return SystemDomainSwitchResult{}, err
	}
	oldCertificate, err := s.store.loadSystemCenterCertificate(ctx, s.store.db, "", oldHostname)
	if err != nil {
		rollbackDNS()
		return SystemDomainSwitchResult{}, fmt.Errorf("center: load current Center certificate: %w", err)
	}
	centerAliases, headscaleAliases, err := s.store.deploymentEndpointAliases(ctx)
	if err != nil {
		rollbackDNS()
		return SystemDomainSwitchResult{}, err
	}
	centerAliases = append(centerAliases, deployapi.CenterEndpointAlias{URL: current.CenterURL, CertificatePEM: oldCertificate.CertificatePEM, CertificateKeyPEM: oldCertificate.PrivateKeyPEM})
	headscaleAliases = append(headscaleAliases, current.HeadscaleURL)
	if err := s.store.reconcileHeadscaleDNSForSystem(ctx, centerURL, []string{current.CenterURL}); err != nil {
		rollbackDNS()
		return SystemDomainSwitchResult{}, fmt.Errorf("center: prepare private DNS for the new domain: %w", err)
	}
	request := deployapi.HeadscaleInstallRequest{
		CenterURL: centerURL, HeadscaleURL: headscaleURL, CenterAliases: centerAliases, HeadscaleAliases: headscaleAliases,
		PublicAddress: binding.PublicAddress, GatewayBindAddress: binding.BindAddress,
		CenterPrivateBindAddress: centerPrivateBindAddress,
		CenterCertificatePEM:     newCertificate.CertificatePEM, CenterCertificateKeyPEM: newCertificate.PrivateKeyPEM,
	}
	if err := s.infrastructure.ReconcileHeadscale(ctx, request); err != nil {
		rollbackDNS()
		restoreErr := s.store.reconcileHeadscaleDNS(context.WithoutCancel(ctx))
		return SystemDomainSwitchResult{}, errors.Join(fmt.Errorf("center: apply the new system domain: %w", err), restoreErr)
	}
	if err := s.store.commitSystemDomainSwitch(ctx, current, selected, zoneName, centerURL, headscaleURL, newCertificate, newDNS); err != nil {
		rollback := deployapi.HeadscaleInstallRequest{
			CenterURL: current.CenterURL, HeadscaleURL: current.HeadscaleURL,
			CenterAliases:    append(centerAliases[:len(centerAliases)-1], deployapi.CenterEndpointAlias{URL: centerURL, CertificatePEM: newCertificate.CertificatePEM, CertificateKeyPEM: newCertificate.PrivateKeyPEM}),
			HeadscaleAliases: append(headscaleAliases[:len(headscaleAliases)-1], headscaleURL),
			PublicAddress:    binding.PublicAddress, GatewayBindAddress: binding.BindAddress,
			CenterPrivateBindAddress: centerPrivateBindAddress,
			CenterCertificatePEM:     oldCertificate.CertificatePEM, CenterCertificateKeyPEM: oldCertificate.PrivateKeyPEM,
		}
		rollbackErr := s.infrastructure.ReconcileHeadscale(context.WithoutCancel(ctx), rollback)
		rollbackDNS()
		restoreErr := s.store.reconcileHeadscaleDNS(context.WithoutCancel(ctx))
		return SystemDomainSwitchResult{}, errors.Join(err, rollbackErr, restoreErr)
	}
	updated, err := s.store.SystemDomain(ctx)
	if err != nil {
		return SystemDomainSwitchResult{}, err
	}
	return SystemDomainSwitchResult{SystemDomainView: updated, PreviousCenterURL: current.CenterURL, PreviousHeadscaleURL: current.HeadscaleURL, BackupCreated: true}, nil
}

func (s *Store) cloudflareForZone(ctx context.Context, zoneID string) (cloudflareClient, CloudflareZone, error) {
	zoneID = strings.TrimSpace(zoneID)
	if zoneID == "" {
		return cloudflareClient{}, CloudflareZone{}, errors.New("center: select a Cloudflare zone")
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return cloudflareClient{}, CloudflareZone{}, err
	}
	zones, err := s.listCloudflareZones(ctx, client.token)
	if err != nil {
		return cloudflareClient{}, CloudflareZone{}, err
	}
	for _, zone := range zones {
		if zone.ID == zoneID {
			client.accountID = zone.AccountID
			client.zoneID = zone.ID
			return client, zone, nil
		}
	}
	return cloudflareClient{}, CloudflareZone{}, errors.New("center: selected Cloudflare zone is not available to the saved authorization")
}

func ensureSystemDNSRecord(ctx context.Context, client cloudflareClient, endpoint, address string) (SetupDNSRecord, bool, error) {
	hostname, err := gatewayEndpointHostname(endpoint)
	if err != nil {
		return SetupDNSRecord{}, false, err
	}
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil {
		return SetupDNSRecord{}, false, errors.New("center: public gateway address must be IPv4")
	}
	recordType := "A"
	existing, err := client.listDNSRecords(ctx, hostname)
	if err != nil {
		return SetupDNSRecord{}, false, err
	}
	if len(existing) != 0 {
		if len(existing) == 1 && existing[0].Type == recordType && existing[0].Content == ip.String() && !existing[0].Proxied {
			return SetupDNSRecord{ID: existing[0].ID, Type: recordType, Name: hostname, Content: ip.String()}, false, nil
		}
		return SetupDNSRecord{}, false, fmt.Errorf("center: DNS record %s already exists with a different value", hostname)
	}
	id, err := client.createDNSRecord(ctx, recordType, hostname, ip.String(), false)
	if err != nil {
		return SetupDNSRecord{}, false, err
	}
	return SetupDNSRecord{ID: id, Type: recordType, Name: hostname, Content: ip.String()}, true, nil
}

func (s *Store) createSystemDomainBackup(ctx context.Context) (string, error) {
	directory := filepath.Join(s.dataDir, "domain-switch-backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("center: create domain switch backup directory: %w", err)
	}
	path := filepath.Join(directory, "center-before-domain-switch-"+s.now().UTC().Format("20060102T150405.000000000Z")+".db")
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("center: create domain switch database backup: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("center: protect domain switch database backup: %w", err)
	}
	return path, nil
}

func (s *Store) commitSystemDomainSwitch(ctx context.Context, current SystemDomainView, selected CloudflareZone, zoneName, centerURL, headscaleURL string, certificate managedCertificate, dns SetupDNSRecord) error {
	encodedCertificate, err := json.Marshal(certificate)
	if err != nil {
		return err
	}
	encodedDNS, err := json.Marshal([]SetupDNSRecord{dns})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldCertificateSecretID, oldCertificateExpiry string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateSecretSetting).Scan(&oldCertificateSecretID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateExpirySetting).Scan(&oldCertificateExpiry); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var replacedAliasSecretID string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(certificate_secret_id, '') FROM system_endpoint_aliases WHERE kind = 'center' AND endpoint = ?`, centerURL).Scan(&replacedAliasSecretID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM system_endpoint_aliases WHERE (kind = 'center' AND endpoint = ?) OR (kind = 'headscale' AND endpoint = ?)`, centerURL, headscaleURL); err != nil {
		return err
	}
	if replacedAliasSecretID != "" && replacedAliasSecretID != oldCertificateSecretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, replacedAliasSecretID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_endpoint_aliases(kind, endpoint, certificate_secret_id, certificate_not_after, created_at, updated_at)
		VALUES('center', ?, ?, ?, ?, ?) ON CONFLICT(kind, endpoint) DO UPDATE SET certificate_secret_id = excluded.certificate_secret_id, certificate_not_after = excluded.certificate_not_after, updated_at = excluded.updated_at`, current.CenterURL, oldCertificateSecretID, oldCertificateExpiry, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO system_endpoint_aliases(kind, endpoint, created_at, updated_at)
		VALUES('headscale', ?, ?, ?) ON CONFLICT(kind, endpoint) DO UPDATE SET updated_at = excluded.updated_at`, current.HeadscaleURL, now, now); err != nil {
		return err
	}
	newCertificateSecretID, err := s.putSecret(ctx, tx, encodedCertificate, systemCenterCertificateContext)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		agentConnectURLSetting:               centerURL,
		systemCenterCertificateSecretSetting: newCertificateSecretID,
		systemCenterCertificateExpirySetting: certificate.NotAfter.Format(time.RFC3339Nano),
		"cloudflare_setup_dns_records":       string(encodedDNS),
		builtinHeadscaleRuntimeSetting:       builtinHeadscaleRuntimeVersion,
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE network_integrations SET endpoint = ?, updated_at = ? WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`, headscaleURL, now); err != nil {
		return err
	}
	if result, err := tx.ExecContext(ctx, `UPDATE network_integrations SET endpoint = ?, account_id = ?, zone_id = ?, last_error = '', updated_at = ? WHERE kind = 'cloudflare' AND mode = 'oauth' AND status = 'configured'`, zoneName, selected.AccountID, selected.ID, now); err != nil {
		return err
	} else if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Cloudflare authorization changed during the domain switch")
	}
	oldNamespace := domainNamespaceFromCenterURL(current.CenterURL)
	newNamespace := domainNamespaceFromCenterURL(centerURL)
	if oldNamespace != "" && oldNamespace != newNamespace {
		rows, err := tx.QueryContext(ctx, `SELECT secret_id FROM site_certificates WHERE site_id IN (SELECT id FROM sites WHERE domain_suffix = ?) AND secret_id IS NOT NULL`, oldNamespace)
		if err != nil {
			return err
		}
		var obsoleteSecrets []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			obsoleteSecrets = append(obsoleteSecrets, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_certificates WHERE site_id IN (SELECT id FROM sites WHERE domain_suffix = ?)`, oldNamespace); err != nil {
			return err
		}
		for _, id := range obsoleteSecrets {
			if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET domain_suffix = ?, updated_at = ? WHERE domain_suffix = ?`, newNamespace, now, oldNamespace); err != nil {
			return err
		}
	}
	if err := s.queueAllGatewayStatesTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit system domain switch: %w", err)
	}
	return nil
}
