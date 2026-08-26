package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/petauron/vastora/internal/platform"
)

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
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		replaceExisting := flags.Bool("replace-existing", false, "replace an existing Center enrollment after explicit confirmation")
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
		store, err := agent.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		existing, connectionErr := store.Connection(context.Background())
		if connectionErr == nil && !*replaceExisting {
			return errors.New("agent is already installed; use agent update or agent configure")
		}
		if connectionErr != nil && *replaceExisting {
			return errors.New("agent cannot replace a Center enrollment because no existing enrollment was found")
		}
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		client := agent.Client{}
		var enrollment agent.Enrollment
		if *replaceExisting {
			enrollment, err = client.MigrateEnrollment(context.Background(), store, *centerURL, token)
		} else {
			enrollment, err = client.Enroll(context.Background(), store, *centerURL, token)
		}
		if err != nil {
			return err
		}
		rollback := func(cause error) error {
			if !*replaceExisting {
				return cause
			}
			if rollbackErr := store.ReplaceConnection(context.Background(), existing); rollbackErr != nil {
				return fmt.Errorf("%w; additionally failed to restore the previous Center connection: %v", cause, rollbackErr)
			}
			return cause
		}
		roles, capabilities, err := validatedNodeRuntime(strings.Join(enrollment.Roles, ","), nodeCapabilitiesString(enrollment.Capabilities))
		if err != nil {
			return rollback(fmt.Errorf("center returned an invalid Agent profile: %w", err))
		}
		executable, err := os.Executable()
		if err != nil {
			return rollback(fmt.Errorf("locate vastora executable: %w", err))
		}
		if err := installSystemdAgent(executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities)); err != nil {
			return rollback(err)
		}
		if *replaceExisting {
			if output, restartErr := exec.Command("systemctl", "restart", "vastora-agent.service").CombinedOutput(); restartErr != nil {
				cause := rollback(fmt.Errorf("restart migrated Agent service: %s: %w", strings.TrimSpace(string(output)), restartErr))
				_, _ = exec.Command("systemctl", "restart", "vastora-agent.service").CombinedOutput()
				return cause
			}
			fmt.Printf("Agent %s moved to the new Center and restarted\n", enrollment.Name)
			return nil
		}
		fmt.Printf("Agent %s installed and started\n", enrollment.Name)
		return nil
	case "status":
		flags := flag.NewFlagSet("agent status", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		connection, err := agent.InspectConnection(*dataDir)
		if err != nil {
			return err
		}
		fmt.Printf("Agent: %s\nCenter: %s\nAgent ID: %s\n", connection.Name, connection.CenterURL, connection.AgentID)
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
		centerURL := flags.String("center-url", "", "current Center HTTPS URL or loopback HTTP URL")
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
		httpClient := &http.Client{Timeout: 2 * time.Minute}
		if strings.TrimSpace(*centerURL) != "" {
			verified, verifyErr := (agent.Client{HTTPClient: httpClient}).VerifyCenterURL(context.Background(), *centerURL)
			if verifyErr != nil {
				return fmt.Errorf("verify requested Center URL: %w", verifyErr)
			}
			if verified != connection.CenterURL {
				connection.CenterURL = verified
				if err := store.ReplaceConnection(context.Background(), connection); err != nil {
					return fmt.Errorf("save requested Center URL: %w", err)
				}
			}
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		version, err := updateAgentExecutable(context.Background(), httpClient, connection, executable, func() error {
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
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" || *centerURL == "" || *tokenFile == "" {
			return errors.New("--data-dir, --center-url, and --token-file are required")
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
		enrollment, err := (agent.Client{}).Enroll(context.Background(), store, *centerURL, token)
		if err != nil {
			return err
		}
		fmt.Printf("Agent %s enrolled with %s\n", enrollment.Name, *centerURL)
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
		haproxyImage := flags.String("haproxy-image", "docker.io/library/haproxy:3.2.7-alpine@sha256:a9b408a818f5d0d9a6a042ec2957921038399f7a515f8b7bfef2054ef7f4ce05", "HAProxy image installed only when this gateway shares public port 443")
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
			caddyDriver, err := agent.NewCaddyGatewayDriver(*caddyAdmin)
			if err != nil {
				return err
			}
			caddyProvisioner := agent.DockerGatewayProvisioner{Image: *caddyImage, AdminListen: caddyDriver.AdminListen, AdminSocketPath: caddyDriver.AdminSocketPath}
			caddyDriver.SystemGateway = caddyProvisioner
			layer4 := agent.DockerLayer4Provisioner{Image: *haproxyImage}
			managedDriver := &agent.ManagedGatewayDriver{Caddy: caddyDriver, Layer4: layer4}
			client.GatewayDriver = managedDriver
			client.GatewayProvisioner = agent.ManagedGatewayProvisioner{
				Caddy:  caddyProvisioner,
				Layer4: layer4,
				Driver: managedDriver,
			}
		}
		if capabilities.Tunnel {
			client.TunnelProvisioner = agent.DockerTunnelProvisioner{}
		}
		go client.RunHeartbeats(context.Background(), store, *heartbeatInterval, func(err error) {
			fmt.Fprintln(os.Stderr, "agent heartbeat:", err)
		})
		go client.RunTasks(context.Background(), store, func(err error) {
			fmt.Fprintln(os.Stderr, "agent task channel:", err)
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
	endpoint, err := agentUpdateEndpoint(connection, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
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

func agentUpdateEndpoint(connection agent.Connection, operatingSystem, architecture string) (string, error) {
	target, err := platform.Parse(operatingSystem, architecture)
	if err != nil {
		return "", fmt.Errorf("agent update target: %w", err)
	}
	return strings.TrimRight(connection.CenterURL, "/") + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/binary/" + target.OS + "/" + target.Architecture, nil
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
