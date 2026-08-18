// Command vastora provides the local control-plane, Agent, and catalog tools.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/center"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}
	switch arguments[0] {
	case "version":
		fmt.Println(center.Version)
		return nil
	case "catalog":
		return runCatalog(arguments[1:])
	case "center":
		return runCenter(arguments[1:])
	case "agent":
		return runAgent(arguments[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	default:
		return usageError()
	}
}

func runCatalog(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("catalog command is required")
	}
	switch arguments[0] {
	case "keygen":
		flags := flag.NewFlagSet("catalog keygen", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		outDir := flags.String("out-dir", "", "directory for the generated Ed25519 key files")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *outDir == "" {
			return errors.New("--out-dir is required")
		}
		return catalogKeygen(*outDir)
	case "validate":
		flags := flag.NewFlagSet("catalog validate", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		input := flags.String("catalog", "", "catalog JSON file")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *input == "" {
			return errors.New("--catalog is required")
		}
		payload, err := os.ReadFile(*input)
		if err != nil {
			return fmt.Errorf("read catalog: %w", err)
		}
		parsed, err := catalog.ParseCatalog(payload)
		if err != nil {
			return err
		}
		fmt.Printf("valid catalog: %d app(s)\n", len(parsed.Apps))
		return nil
	case "sign":
		flags := flag.NewFlagSet("catalog sign", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		input := flags.String("catalog", "", "catalog JSON file")
		privateKeyPath := flags.String("private-key", "", "Ed25519 private key file")
		keyID := flags.String("key-id", "", "catalog signing key identifier")
		output := flags.String("output", "", "output envelope path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *input == "" || *privateKeyPath == "" || *keyID == "" || *output == "" {
			return errors.New("--catalog, --private-key, --key-id, and --output are required")
		}
		return catalogSign(*input, *privateKeyPath, *keyID, *output)
	default:
		return errors.New("unknown catalog command")
	}
}

func runCenter(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("center command is required")
	}
	switch arguments[0] {
	case "agent-token":
		if len(arguments) < 2 || arguments[1] != "create" {
			return errors.New("center agent-token create is required")
		}
		flags := flag.NewFlagSet("center agent-token create", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		siteID := flags.String("site-id", "", "target Site ID")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if *dataDir == "" || *siteID == "" {
			return errors.New("--data-dir and --site-id are required")
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		enrollment, err := store.CreateAgentEnrollment(context.Background(), *siteID)
		if err != nil {
			return err
		}
		fmt.Printf("token: %s\nexpires-at: %s\n", enrollment.Token, enrollment.ExpiresAt.Format(time.RFC3339))
		return nil
	case "serve":
		flags := flag.NewFlagSet("center serve", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		listen := flags.String("listen", "127.0.0.1:8080", "listen address")
		webDir := flags.String("web-dir", "web/dist", "compiled React web directory")
		officialCatalog := flags.String("official-catalog", "catalog/catalog.json", "official Catalog JSON file")
		agentBinariesDir := flags.String("agent-binaries-dir", "agent-binaries", "directory containing linux-amd64 and linux-arm64 Agent binaries")
		agentConnectURL := flags.String("agent-connect-url", "", "Agent-reachable Center URL suggested during first setup")
		tlsCert := flags.String("tls-cert", "", "PEM certificate path")
		tlsKey := flags.String("tls-key", "", "PEM private key path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" || *agentConnectURL == "" {
			return errors.New("--data-dir and --agent-connect-url are required")
		}
		if (*tlsCert == "") != (*tlsKey == "") {
			return errors.New("--tls-cert and --tls-key must be provided together")
		}
		normalizedAgentConnectURL, err := center.NormalizeAgentConnectURL(*agentConnectURL)
		if err != nil {
			return err
		}
		if *tlsCert == "" && !loopbackAddress(*listen) {
			return errors.New("refusing a non-loopback HTTP listener; provide TLS certificate and key")
		}
		catalogPayload, err := os.ReadFile(*officialCatalog)
		if err != nil {
			return fmt.Errorf("read official catalog: %w", err)
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.SeedOfficialCatalog(context.Background(), catalogPayload); err != nil {
			return err
		}
		handler := center.NewServer(store, *webDir, *tlsCert != "").
			WithOfficialCatalog(catalogPayload).
			WithAgentBinaries(*agentBinariesDir).
			WithSetupAgentConnectURL(normalizedAgentConnectURL).
			Handler()
		server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		fmt.Printf("Center listening on %s\n", *listen)
		if *tlsCert != "" {
			err = server.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = server.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "backup":
		flags := flag.NewFlagSet("center backup", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		output := flags.String("output", "", "encrypted backup file")
		passwordFile := flags.String("password-file", "", "0600 file containing the backup password")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" || *output == "" || *passwordFile == "" {
			return errors.New("--data-dir, --output, and --password-file are required")
		}
		password, err := readPrivatePassword(*passwordFile)
		if err != nil {
			return err
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Backup(context.Background(), *output, password); err != nil {
			return err
		}
		fmt.Printf("Encrypted Center backup written to %s\n", *output)
		return nil
	case "restore":
		flags := flag.NewFlagSet("center restore", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		input := flags.String("input", "", "encrypted backup file")
		dataDir := flags.String("data-dir", "", "new empty Center state directory")
		passwordFile := flags.String("password-file", "", "0600 file containing the backup password")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *input == "" || *dataDir == "" || *passwordFile == "" {
			return errors.New("--input, --data-dir, and --password-file are required")
		}
		password, err := readPrivatePassword(*passwordFile)
		if err != nil {
			return err
		}
		if err := center.Restore(*input, *dataDir, password); err != nil {
			return err
		}
		fmt.Printf("Center backup restored to %s\n", *dataDir)
		return nil
	default:
		return errors.New("unknown center command")
	}
}

func runAgent(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("agent command is required")
	}
	switch arguments[0] {
	case "install":
		flags := flag.NewFlagSet("agent install", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		name := flags.String("name", "", "agent display name")
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		rolesValue := flags.String("roles", "worker,gateway", "comma-separated node roles: worker,gateway")
		capabilitiesValue := flags.String("capabilities", "docker,gateway,tunnel", "comma-separated implemented capabilities: docker,gateway,tunnel")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if runtime.GOOS != "linux" {
			return errors.New("agent install currently supports Linux systemd hosts")
		}
		if os.Geteuid() != 0 {
			return errors.New("agent install must run as root")
		}
		if *centerURL == "" || *tokenFile == "" {
			return errors.New("--center-url and --token-file are required")
		}
		if *name == "" {
			*name, _ = os.Hostname()
		}
		roles, err := parseNodeRoles(*rolesValue)
		if err != nil {
			return err
		}
		capabilities, err := parseNodeCapabilities(*capabilitiesValue)
		if err != nil {
			return err
		}
		if capabilities.Docker && !containsValue(roles, "worker") || capabilities.Gateway && !containsValue(roles, "gateway") || capabilities.Tunnel && !containsValue(roles, "worker") {
			return errors.New("selected capabilities do not match the node roles")
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if _, connectionErr := store.Connection(context.Background()); connectionErr != nil {
			token, tokenErr := readPrivateToken(*tokenFile)
			if tokenErr != nil {
				return tokenErr
			}
			if err := (agent.Client{}).Enroll(context.Background(), store, *centerURL, *name, token); err != nil {
				return err
			}
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		if err := installSystemdAgent(executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities)); err != nil {
			return err
		}
		fmt.Printf("Agent %s installed and started\n", *name)
		return nil
	case "configure":
		flags := flag.NewFlagSet("agent configure", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		rolesValue := flags.String("roles", "", "comma-separated node roles: worker,gateway")
		capabilitiesValue := flags.String("capabilities", "", "comma-separated implemented capabilities: docker,gateway,tunnel")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent configure"); err != nil {
			return err
		}
		if *rolesValue == "" || *capabilitiesValue == "" {
			return errors.New("--roles and --capabilities are required")
		}
		roles, capabilities, err := validatedNodeRuntime(*rolesValue, *capabilitiesValue)
		if err != nil {
			return err
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if _, err := store.Connection(context.Background()); err != nil {
			return errors.New("agent must be enrolled before its purpose can be changed")
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		if err := installSystemdAgent(executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities)); err != nil {
			return err
		}
		if output, err := exec.Command("systemctl", "restart", "vastora-agent.service").CombinedOutput(); err != nil {
			return fmt.Errorf("restart Agent service: %s: %w", strings.TrimSpace(string(output)), err)
		}
		fmt.Println("Agent purpose updated; Center will refresh it after the next heartbeat")
		return nil
	case "update":
		flags := flag.NewFlagSet("agent update", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent update"); err != nil {
			return err
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		connection, err := store.Connection(context.Background())
		if err != nil {
			return errors.New("agent must be enrolled before it can update")
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		version, err := updateAgentExecutable(context.Background(), &http.Client{Timeout: 2 * time.Minute}, connection, executable, func() error {
			output, restartErr := exec.Command("systemctl", "restart", "vastora-agent.service").CombinedOutput()
			if restartErr != nil {
				return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), restartErr)
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Printf("Agent updated to %s and restarted\n", version)
		return nil
	case "enroll":
		flags := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		name := flags.String("name", "", "agent display name")
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" || *centerURL == "" || *tokenFile == "" {
			return errors.New("--data-dir, --center-url, and --token-file are required")
		}
		if *name == "" {
			*name, _ = os.Hostname()
		}
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := (agent.Client{}).Enroll(context.Background(), store, *centerURL, *name, token); err != nil {
			return err
		}
		fmt.Printf("Agent %s enrolled with %s\n", *name, *centerURL)
		return nil
	case "init":
		flags := flag.NewFlagSet("agent init", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Agent state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		fmt.Printf("Agent state initialized at %s\n", *dataDir)
		return nil
	case "serve":
		flags := flag.NewFlagSet("agent serve", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Agent state directory")
		listen := flags.String("listen", "127.0.0.1:8090", "loopback health listener")
		heartbeatInterval := flags.Duration("heartbeat-interval", 15*time.Second, "Center heartbeat interval")
		rolesValue := flags.String("roles", "worker", "comma-separated node roles: worker,gateway")
		capabilitiesValue := flags.String("capabilities", "docker", "comma-separated implemented capabilities: docker,gateway")
		caddyAdmin := flags.String("caddy-admin", "unix:///run/vastora/caddy-admin.sock", "private Caddy Admin API endpoint for gateway nodes")
		caddyImage := flags.String("caddy-image", "docker.io/library/caddy:2.11.4@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d", "Caddy image installed by the Agent when this node is selected as a gateway")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		if !loopbackAddress(*listen) {
			return errors.New("agent health listener must be loopback-only")
		}
		roles, err := parseNodeRoles(*rolesValue)
		if err != nil {
			return err
		}
		capabilities, err := parseNodeCapabilities(*capabilitiesValue)
		if err != nil {
			return err
		}
		if capabilities.Docker && !containsValue(roles, "worker") {
			return errors.New("docker capability requires the worker role")
		}
		if capabilities.Gateway && !containsValue(roles, "gateway") {
			return errors.New("gateway capability requires the gateway role")
		}
		if capabilities.Tunnel && !containsValue(roles, "worker") {
			return errors.New("tunnel capability requires the worker role")
		}
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if _, err := store.Connection(context.Background()); err != nil {
			return err
		}
		client := agent.Client{Roles: roles, Capabilities: capabilities}
		if capabilities.Docker {
			client.Executor = agent.DockerExecutor{}
		}
		if capabilities.Gateway {
			gatewayDriver, err := agent.NewCaddyGatewayDriver(*caddyAdmin)
			if err != nil {
				return err
			}
			client.GatewayDriver = gatewayDriver
			client.GatewayProvisioner = agent.DockerGatewayProvisioner{
				Image: *caddyImage, AdminListen: gatewayDriver.AdminListen, AdminSocketPath: gatewayDriver.AdminSocketPath,
			}
		}
		if capabilities.Tunnel {
			client.TunnelProvisioner = agent.DockerTunnelProvisioner{}
		}
		go client.RunHeartbeats(context.Background(), store, *heartbeatInterval, func(err error) {
			fmt.Fprintln(os.Stderr, "agent heartbeat:", err)
		})
		fmt.Printf("Agent health listener on %s\n", *listen)
		return http.ListenAndServe(*listen, store.Handler())
	default:
		return errors.New("unknown agent command")
	}
}

func requireLinuxRoot(command string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s currently supports Linux systemd hosts", command)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s must run as root", command)
	}
	return nil
}

func validatedNodeRuntime(rolesValue, capabilitiesValue string) ([]string, agent.Capabilities, error) {
	roles, err := parseNodeRoles(rolesValue)
	if err != nil {
		return nil, agent.Capabilities{}, err
	}
	capabilities, err := parseNodeCapabilities(capabilitiesValue)
	if err != nil {
		return nil, agent.Capabilities{}, err
	}
	if capabilities.Docker && !containsValue(roles, "worker") || capabilities.Gateway && !containsValue(roles, "gateway") || capabilities.Tunnel && !containsValue(roles, "worker") {
		return nil, agent.Capabilities{}, errors.New("selected capabilities do not match the node roles")
	}
	return roles, capabilities, nil
}

func updateAgentExecutable(ctx context.Context, client *http.Client, connection agent.Connection, executable string, restart func() error) (string, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", errors.New("agent update is available only for amd64 and arm64")
	}
	endpoint := strings.TrimRight(connection.CenterURL, "/") + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/binary/linux/" + runtime.GOARCH
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create Agent update request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+connection.Credential)
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download Agent update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("download Agent update: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	expectedVersion := strings.TrimSpace(response.Header.Get("X-Vastora-Version"))
	expectedDigest := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Vastora-SHA256")))
	if expectedVersion == "" || len(expectedDigest) != sha256.Size*2 {
		return "", errors.New("center returned incomplete Agent update metadata")
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve Agent executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agent executable is not a regular file")
	}
	temporary, err := os.CreateTemp(filepath.Dir(executable), ".vastora-update-*")
	if err != nil {
		return "", fmt.Errorf("create Agent update file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, (256<<20)+1))
	if copyErr == nil && written > 256<<20 {
		copyErr = errors.New("agent update exceeds 256 MiB")
	}
	if syncErr := temporary.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := temporary.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return "", fmt.Errorf("store Agent update: %w", copyErr)
	}
	if got := fmt.Sprintf("%x", digest.Sum(nil)); got != expectedDigest {
		return "", errors.New("agent update integrity check failed")
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return "", fmt.Errorf("make Agent update executable: %w", err)
	}
	output, err := exec.CommandContext(ctx, temporaryPath, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != expectedVersion {
		return "", errors.New("downloaded Agent update failed its version check")
	}
	backupPath := executable + ".previous"
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("prepare Agent rollback file: %w", err)
	}
	if err := os.Rename(executable, backupPath); err != nil {
		return "", fmt.Errorf("preserve previous Agent binary: %w", err)
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		_ = os.Rename(backupPath, executable)
		return "", fmt.Errorf("install Agent update: %w", err)
	}
	if err := restart(); err != nil {
		_ = os.Rename(executable, temporaryPath)
		_ = os.Rename(backupPath, executable)
		_ = restart()
		return "", fmt.Errorf("restart updated Agent; previous binary restored: %w", err)
	}
	return expectedVersion, nil
}

const vastoraAgentUnitPath = "/etc/systemd/system/vastora-agent.service"

func installSystemdAgent(executable, dataDir, roles, capabilities string) error {
	executable, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve Agent data path: %w", err)
	}
	unit := systemdAgentUnit(executable, dataDir, roles, capabilities)
	if existing, readErr := os.ReadFile(vastoraAgentUnitPath); readErr == nil && !strings.Contains(string(existing), "Description=Vastora Agent") {
		return errors.New("refusing to replace an unrelated vastora-agent.service")
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("inspect systemd service: %w", readErr)
	}
	temporary := vastoraAgentUnitPath + ".tmp"
	if err := os.WriteFile(temporary, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}
	if err := os.Rename(temporary, vastoraAgentUnitPath); err != nil {
		return fmt.Errorf("install systemd service: %w", err)
	}
	if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if output, err := exec.Command("systemctl", "enable", "--now", "vastora-agent.service").CombinedOutput(); err != nil {
		return fmt.Errorf("start Agent service: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func systemdAgentUnit(executable, dataDir, roles, capabilities string) string {
	return `[Unit]
Description=Vastora Agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(executable) + ` agent serve --data-dir ` + systemdQuote(dataDir) + ` --roles ` + systemdQuote(roles) + ` --capabilities ` + systemdQuote(capabilities) + `
Restart=always
RestartSec=5s
TimeoutStopSec=30s

[Install]
WantedBy=multi-user.target
`
}

func systemdQuote(value string) string { return strconv.Quote(value) }

func nodeCapabilitiesString(value agent.Capabilities) string {
	capabilities := make([]string, 0, 3)
	if value.Docker {
		capabilities = append(capabilities, "docker")
	}
	if value.Gateway {
		capabilities = append(capabilities, "gateway")
	}
	if value.Tunnel {
		capabilities = append(capabilities, "tunnel")
	}
	return strings.Join(capabilities, ",")
}

func parseNodeRoles(raw string) ([]string, error) {
	roles := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		role := strings.TrimSpace(value)
		if role == "" || seen[role] {
			continue
		}
		switch role {
		case "worker":
		case "gateway":
		default:
			return nil, fmt.Errorf("unsupported node role %q", role)
		}
		seen[role] = true
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		return nil, errors.New("at least one node role is required")
	}
	return roles, nil
}

func parseNodeCapabilities(raw string) (agent.Capabilities, error) {
	capabilities := agent.Capabilities{}
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		capability := strings.TrimSpace(value)
		if capability == "" || seen[capability] {
			continue
		}
		switch capability {
		case "docker":
			capabilities.Docker = true
		case "gateway":
			capabilities.Gateway = true
		case "tunnel":
			capabilities.Tunnel = true
		case "metrics", "logs":
			return agent.Capabilities{}, fmt.Errorf("capability %q is reserved but not implemented", capability)
		default:
			return agent.Capabilities{}, fmt.Errorf("unsupported node capability %q", capability)
		}
		seen[capability] = true
	}
	if !capabilities.Docker && !capabilities.Gateway && !capabilities.Tunnel {
		return agent.Capabilities{}, errors.New("at least one implemented node capability is required")
	}
	return capabilities, nil
}

func containsValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func catalogKeygen(outDir string) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Ed25519 key: %w", err)
	}
	privatePath := filepath.Join(outDir, "catalog-signing-private.key")
	publicPath := filepath.Join(outDir, "catalog-signing-public.key")
	if err := writeNewFile(privatePath, []byte(base64.RawURLEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		return err
	}
	if err := writeNewFile(publicPath, []byte(base64.RawURLEncoding.EncodeToString(publicKey)+"\n"), 0o644); err != nil {
		return err
	}
	fingerprint := sha256Sum(publicKey)
	fmt.Printf("private key: %s\npublic key: %s\npublic key fingerprint: %s\n", privatePath, publicPath, fingerprint)
	return nil
}

func catalogSign(inputPath, privateKeyPath, keyID, outputPath string) error {
	payload, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	encodedPrivateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	privateKey, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(encodedPrivateKey)))
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("private key is not a valid Ed25519 key")
	}
	envelope, err := catalog.Sign(keyID, ed25519.PrivateKey(privateKey), payload)
	if err != nil {
		return err
	}
	encoded, err := catalog.MarshalEnvelope(envelope)
	if err != nil {
		return err
	}
	return writeNewFile(outputPath, append(encoded, '\n'), 0o644)
}

func writeNewFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func sha256Sum(value []byte) string {
	sum := sha256.Sum256(value)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func readPrivatePassword(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("password file must be a regular file with mode 0600")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	password := strings.TrimRight(string(content), "\r\n")
	if password == "" {
		return "", errors.New("password file is empty")
	}
	return password, nil
}

func readPrivateToken(path string) (string, error) {
	if path == "-" {
		content, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return "", fmt.Errorf("read enrollment token: %w", err)
		}
		token := strings.TrimSpace(string(content))
		if token == "" {
			return "", errors.New("enrollment token is empty")
		}
		return token, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read enrollment token: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("enrollment token file must be a regular file with mode 0600")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read enrollment token: %w", err)
	}
	token := strings.TrimSpace(string(content))
	if token == "" {
		return "", errors.New("enrollment token is empty")
	}
	return token, nil
}

func usageError() error {
	printUsage(os.Stderr)
	return errors.New("invalid command")
}

func printUsage(writer *os.File) {
	fmt.Fprint(writer, `Vastora control-plane tools

Usage:
  vastora version
  vastora center serve --data-dir DIR --agent-connect-url URL [--listen 127.0.0.1:8080] [--tls-cert CERT --tls-key KEY]
  vastora center agent-token create --data-dir DIR --site-id SITE
  vastora center backup --data-dir DIR --output FILE --password-file FILE
  vastora center restore --input FILE --data-dir NEW_DIR --password-file FILE
  vastora agent init --data-dir DIR
  vastora agent enroll --data-dir DIR --center-url URL --token-file FILE [--name NAME]
  vastora agent install --center-url URL --token-file FILE [--name NAME]
  vastora agent configure --roles worker[,gateway] --capabilities docker[,gateway,tunnel]
  vastora agent update [--data-dir /var/lib/vastora/agent]
  vastora agent serve --data-dir DIR [--listen 127.0.0.1:8090]
  vastora catalog keygen --out-dir DIR
  vastora catalog validate --catalog FILE
  vastora catalog sign --catalog FILE --private-key FILE --key-id ID --output FILE
`)
}
