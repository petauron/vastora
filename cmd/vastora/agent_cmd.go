package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/petauron/vastora/internal/controlplane"
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
		caFingerprint := flags.String("ca-fingerprint", "", "expected Center CA SHA-256 public-key fingerprint")
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
		hasConnection, err := store.HasConnection(context.Background())
		if err != nil {
			return err
		}
		operation, operationExists, operationErr := store.InstallOperation(context.Background())
		if operationErr != nil {
			return operationErr
		}
		if hasConnection && !*replaceExisting && !operationExists {
			return errors.New("agent is already installed; use agent update or agent configure")
		}
		if !hasConnection && *replaceExisting && !operationExists {
			return errors.New("agent cannot replace a Center enrollment because no existing enrollment was found")
		}
		if operationExists && operation.ReplaceExisting != *replaceExisting {
			return errors.New("agent installation is incomplete; rerun it with the original replacement choice")
		}
		token, err := readPrivateToken(*tokenFile)
		if err != nil {
			return err
		}
		client := agent.Client{}
		var enrollment agent.Enrollment
		if *replaceExisting {
			enrollment, err = client.MigrateEnrollment(context.Background(), store, *centerURL, token, *caFingerprint)
		} else {
			enrollment, err = client.Enroll(context.Background(), store, *centerURL, token, *caFingerprint)
		}
		if err != nil {
			return err
		}
		roles, capabilities, err := validatedNodeRuntime(strings.Join(enrollment.Roles, ","), nodeCapabilitiesString(enrollment.Capabilities))
		if err != nil {
			return fmt.Errorf("center returned an invalid Agent profile: %w", err)
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		if err := resumeSystemdAgentInstall(context.Background(), store, executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities), *replaceExisting, defaultAgentSystemdInstallEnvironment()); err != nil {
			return err
		}
		if *replaceExisting {
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
	case "install-state":
		flags := flag.NewFlagSet("agent install-state", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		operation, exists, err := agent.InspectInstallOperation(*dataDir)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Println("none")
		} else if operation.ReplaceExisting {
			fmt.Println("replace")
		} else {
			fmt.Println("fresh")
		}
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
	case "configure-center":
		flags := flag.NewFlagSet("agent configure-center", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		caFingerprint := flags.String("ca-fingerprint", "", "expected Center CA SHA-256 public-key fingerprint")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent configure-center"); err != nil {
			return err
		}
		if *centerURL == "" || flags.NArg() != 0 {
			return errors.New("--center-url is required")
		}
		if err := configureAgentCenter(context.Background(), *dataDir, *centerURL, *caFingerprint); err != nil {
			return err
		}
		fmt.Println("Agent Center connection updated")
		return nil
	case "update":
		flags := flag.NewFlagSet("agent update", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		centerURL := flags.String("center-url", "", "current Center HTTPS URL or loopback HTTP URL")
		caFingerprint := flags.String("ca-fingerprint", "", "expected CA fingerprint when changing the Center URL")
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
		if strings.TrimSpace(*centerURL) != "" {
			verified, verifyErr := (agent.Client{}).VerifyCenterURL(context.Background(), *centerURL, *caFingerprint)
			if verifyErr != nil {
				return fmt.Errorf("verify requested Center URL: %w", verifyErr)
			}
			if verified.URL != connection.CenterURL || verified.CAFingerprint != connection.CAFingerprint {
				connection.CenterURL = verified.URL
				connection.CAFingerprint = verified.CAFingerprint
				if err := store.ReplaceConnection(context.Background(), connection); err != nil {
					return fmt.Errorf("save requested Center URL: %w", err)
				}
			}
		}
		httpClient, err := agent.CenterHTTPClient(connection, 2*time.Minute)
		if err != nil {
			return err
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
	case "uninstall":
		flags := flag.NewFlagSet("agent uninstall", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		purge := flags.Bool("purge", false, "remove the local Agent and Vastora-owned host dependencies")
		deleteData := flags.Bool("delete-data", false, "permanently delete managed application data")
		runtimeCleaned := flags.Bool("runtime-cleaned", false, "internal: managed runtime was already removed")
		keepBinary := flags.Bool("keep-binary", false, "internal: keep the shared host command")
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !*purge {
			return errors.New("usage: vastora agent uninstall --purge")
		}
		if err := requireLinuxRoot("agent uninstall"); err != nil {
			return err
		}
		if err := uninstallAgentHost(context.Background(), *dataDir, *deleteData, *runtimeCleaned, *keepBinary); err != nil {
			return err
		}
		fmt.Println("Vastora Agent and its managed host state were removed")
		return nil
	case "finish-decommission":
		flags := flag.NewFlagSet("agent finish-decommission", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		operationFile := flags.String("operation-file", hostDecommissionOperationPath, "internal: protected host cleanup operation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("invalid persistent host cleanup command")
		}
		if err := requireLinuxRoot("agent finish-decommission"); err != nil {
			return err
		}
		return runPersistentHostDecommission(context.Background(), *operationFile)
	case "cleanup-decommission":
		flags := flag.NewFlagSet("agent cleanup-decommission", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		operationFile := flags.String("operation-file", hostDecommissionOperationPath, "internal: protected host cleanup operation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("invalid persistent host cleanup finalizer")
		}
		if err := requireLinuxRoot("agent cleanup-decommission"); err != nil {
			return err
		}
		return cleanPersistentHostDecommission(*operationFile)
	case "finish-update":
		flags := flag.NewFlagSet("agent finish-update", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		operationFile := flags.String("operation-file", hostUpdateOperationPath, "internal: protected host update operation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("invalid persistent host update command")
		}
		if err := requireLinuxRoot("agent finish-update"); err != nil {
			return err
		}
		return runPersistentHostUpdate(context.Background(), *operationFile)
	case "cleanup-update":
		flags := flag.NewFlagSet("agent cleanup-update", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		operationFile := flags.String("operation-file", hostUpdateOperationPath, "internal: protected host update operation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("invalid persistent host update finalizer")
		}
		if err := requireLinuxRoot("agent cleanup-update"); err != nil {
			return err
		}
		return cleanPersistentHostUpdate(*operationFile)
	case "enroll":
		flags := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		caFingerprint := flags.String("ca-fingerprint", "", "expected Center CA SHA-256 public-key fingerprint")
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
		enrollment, err := (agent.Client{}).Enroll(context.Background(), store, *centerURL, token, *caFingerprint)
		if err != nil {
			return err
		}
		if err := store.CompleteEnrollmentOnlyOperation(context.Background()); err != nil {
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
	case "switch-control-plane":
		if len(arguments) < 2 {
			return errors.New("agent switch-control-plane action is required")
		}
		flags := flag.NewFlagSet("agent switch-control-plane "+arguments[1], flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		targetCenterURL := flags.String("target-center-url", "", "requested Center URL")
		tailscaleOwnership := flags.String("tailscale-ownership", "", "Tailscale ownership after a successful switch")
		tailscaleEnrolled := flags.Bool("tailscale-enrolled", false, "record a successful private-network enrollment")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent switch-control-plane"); err != nil {
			return err
		}
		environment := defaultControlPlaneSwitchEnvironment(*dataDir)
		switch arguments[1] {
		case "begin":
			if *targetCenterURL == "" || flags.NArg() != 0 {
				return errors.New("usage: vastora agent switch-control-plane begin --target-center-url URL")
			}
			complete, err := beginControlPlaneSwitch(context.Background(), *dataDir, *targetCenterURL, environment)
			if err != nil {
				return err
			}
			if complete {
				fmt.Println("complete")
			} else {
				fmt.Println("pending")
			}
			return nil
		case "rollback":
			if flags.NArg() != 0 {
				return errors.New("usage: vastora agent switch-control-plane rollback")
			}
			return rollbackControlPlaneSwitch(context.Background(), *dataDir, environment)
		case "commit":
			if *tailscaleOwnership == "" || flags.NArg() != 0 {
				return errors.New("usage: vastora agent switch-control-plane commit --tailscale-ownership auto|managed|external|none")
			}
			return commitControlPlaneSwitch(*dataDir, *tailscaleOwnership, *tailscaleEnrolled, environment)
		default:
			return errors.New("agent switch-control-plane action must be begin, rollback, or commit")
		}
	case "prepare-tailscale":
		flags := flag.NewFlagSet("agent prepare-tailscale", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		controlURL := flags.String("control-url", "", "Headscale HTTPS control-plane URL")
		configureOnly := flags.Bool("configure-only", false, "write isolation controls without starting Tailscale")
		var controlAddresses stringListFlag
		flags.Var(&controlAddresses, "control-address", "verified Headscale control-plane IP address")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent prepare-tailscale"); err != nil {
			return err
		}
		if *controlURL == "" {
			return errors.New("--control-url is required")
		}
		return reconcileTailscaleIsolation(context.Background(), agent.TailscaleIsolationDesiredState{ControlURL: *controlURL, ControlAddresses: controlAddresses}, *configureOnly, defaultTailscaleIsolationEnvironment())
	case "adopt-tailscale":
		flags := flag.NewFlagSet("agent adopt-tailscale", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		confirm := flags.Bool("confirm-vastora-ownership", false, "confirm explicit adoption after provenance checks")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent adopt-tailscale"); err != nil {
			return err
		}
		if !*confirm || flags.NArg() != 0 {
			return errors.New("usage: vastora agent adopt-tailscale --confirm-vastora-ownership")
		}
		if err := adoptLegacyVastoraTailscale(context.Background(), defaultTailscaleAdoptionEnvironment(*dataDir)); err != nil {
			return err
		}
		fmt.Println("Verified the older Vastora installation; Tailscale is now managed by this Agent")
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
		haproxyImage := flags.String("haproxy-image", agent.DefaultHAProxyImage, "HAProxy image installed only when this gateway shares public port 443")
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
		hostState, err := agent.ReadHostInstallState(*dataDir)
		if err != nil {
			return err
		}
		client := agent.Client{Roles: roles, Capabilities: capabilities, TailscaleEnrolled: hostState.TailscaleEnrolled, TailscaleOwnership: hostState.TailscaleOwnership}
		client.PublicEgress = agent.NewPublicEgressObserver(&http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second})
		if runtime.GOOS == "linux" {
			if _, lookupErr := exec.LookPath("tailscale"); lookupErr == nil {
				client.TailscaleIsolation = func(ctx context.Context, desired agent.TailscaleIsolationDesiredState) error {
					if err := reconcileTailscaleIsolation(ctx, desired, false, defaultTailscaleIsolationEnvironment()); err != nil {
						return err
					}
					if hostState.TailscaleOwnership != "managed" {
						return nil
					}
					return reconcileTailscaleEndpoint(ctx, desired.StaticEndpoints, defaultTailscaleEndpointEnvironment())
				}
			} else if !errors.Is(lookupErr, exec.ErrNotFound) {
				return fmt.Errorf("locate Tailscale: %w", lookupErr)
			}
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		client.Decommissioner = systemHostDecommissioner{dataDir: *dataDir, executable: executable}
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			client.Updater = systemHostUpdater{dataDir: *dataDir, executable: executable}
		}
		client.Executor = agent.ApplicationExecutor{Host: agent.SystemdHostApplicationManager{}}
		if capabilities.Gateway {
			caddyDriver, err := agent.NewCaddyGatewayDriver(*caddyAdmin)
			if err != nil {
				return err
			}
			caddyProvisioner := agent.DockerGatewayProvisioner{Image: *caddyImage, AdminListen: caddyDriver.AdminListen, AdminSocketPath: caddyDriver.AdminSocketPath}
			caddyDriver.SystemGateway = caddyProvisioner
			layer4 := agent.DockerLayer4Provisioner{Image: *haproxyImage}
			managedDriver := &agent.ManagedGatewayDriver{Caddy: caddyDriver, Layer4: layer4, Runtime: caddyProvisioner}
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
		controlLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		restoreContext, restoreCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err = client.PrepareGatewayStartup(restoreContext, store)
		restoreCancel()
		if err != nil {
			return fmt.Errorf("restore Gateway before starting the Agent control plane: %w", err)
		}
		if err := client.Heartbeat(context.Background(), store); err != nil {
			controlLogger.Error("Initial Agent heartbeat failed", "event", "control_plane.heartbeat", "error", controlplane.SafeError(err.Error()))
		}
		go client.RunHeartbeats(context.Background(), store, *heartbeatInterval, func(err error) {
			controlLogger.Error("Agent heartbeat failed", "event", "control_plane.heartbeat", "error", controlplane.SafeError(err.Error()))
		})
		go client.RunTasks(context.Background(), store, func(err error) {
			controlLogger.Error("Agent task channel failed", "event", "control_plane.task", "error", controlplane.SafeError(err.Error()))
		})
		fmt.Printf("Agent health listener on %s\n", *listen)
		return http.ListenAndServe(*listen, store.Handler())
	default:
		return errors.New("unknown agent command")
	}
}

func configureAgentCenter(ctx context.Context, dataDir, centerURL, caFingerprint string) error {
	store, err := agent.Open(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	connection, err := store.Connection(ctx)
	if err != nil {
		return errors.New("agent must be enrolled before its Center can be changed")
	}
	verified, err := (agent.Client{}).VerifyCenterURL(ctx, centerURL, caFingerprint)
	if err != nil {
		return fmt.Errorf("verify requested Center URL: %w", err)
	}
	if verified.URL == connection.CenterURL && verified.CAFingerprint == connection.CAFingerprint {
		if agentLoopbackCenterURL(verified.URL) {
			return store.SetLocalCenterChannel(verified.URL)
		}
		return store.SetLocalCenterChannel("")
	}
	previous := connection
	connection.CenterURL = verified.URL
	connection.CAFingerprint = verified.CAFingerprint
	if err := store.ReplaceConnection(ctx, connection); err != nil {
		return fmt.Errorf("save requested Center URL: %w", err)
	}
	marker := ""
	if agentLoopbackCenterURL(verified.URL) {
		marker = verified.URL
	}
	if err := store.SetLocalCenterChannel(marker); err != nil {
		if restoreErr := store.ReplaceConnection(ctx, previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore previous Center connection: %w", restoreErr))
		}
		return err
	}
	return nil
}

func agentLoopbackCenterURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	address := net.ParseIP(parsed.Hostname())
	return address != nil && address.IsLoopback()
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
	executable, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve Agent executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agent executable is not a regular file")
	}
	temporaryPath, expectedVersion, err := downloadAgentUpdateCandidate(ctx, client, connection, filepath.Dir(executable))
	if err != nil {
		return "", err
	}
	defer os.Remove(temporaryPath)
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

func downloadAgentUpdateCandidate(ctx context.Context, client *http.Client, connection agent.Connection, directory string) (string, string, error) {
	endpoint, err := agentUpdateEndpoint(connection, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("create Agent update request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+connection.Credential)
	response, err := client.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("download Agent update: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return "", "", fmt.Errorf("download Agent update: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	expectedVersion := strings.TrimSpace(response.Header.Get("X-Vastora-Version"))
	expectedDigest := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Vastora-SHA256")))
	if expectedVersion == "" || len(expectedDigest) != sha256.Size*2 {
		return "", "", errors.New("center returned incomplete Agent update metadata")
	}
	temporary, err := os.CreateTemp(directory, ".vastora-update-*")
	if err != nil {
		return "", "", fmt.Errorf("create Agent update file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
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
		return "", "", fmt.Errorf("store Agent update: %w", copyErr)
	}
	if got := fmt.Sprintf("%x", digest.Sum(nil)); got != expectedDigest {
		return "", "", errors.New("agent update integrity check failed")
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return "", "", fmt.Errorf("make Agent update executable: %w", err)
	}
	output, err := exec.CommandContext(ctx, temporaryPath, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != expectedVersion {
		return "", "", errors.New("downloaded Agent update failed its version check")
	}
	keep = true
	return temporaryPath, expectedVersion, nil
}

func agentUpdateEndpoint(connection agent.Connection, operatingSystem, architecture string) (string, error) {
	target, err := platform.Parse(operatingSystem, architecture)
	if err != nil {
		return "", fmt.Errorf("agent update target: %w", err)
	}
	return strings.TrimRight(connection.CenterURL, "/") + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/binary/" + target.OS + "/" + target.Architecture, nil
}

const vastoraAgentUnitPath = "/etc/systemd/system/vastora-agent.service"

type agentSystemdInstallEnvironment struct {
	unitPath     string
	readFile     func(string) ([]byte, error)
	writeFile    func(string, []byte, os.FileMode) error
	rename       func(string, string) error
	runSystemctl func(...string) ([]byte, error)
	verify       func(context.Context, *agent.Store, string, string) error
}

func defaultAgentSystemdInstallEnvironment() agentSystemdInstallEnvironment {
	return agentSystemdInstallEnvironment{
		unitPath:  vastoraAgentUnitPath,
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
		rename:    os.Rename,
		runSystemctl: func(arguments ...string) ([]byte, error) {
			return exec.Command("systemctl", arguments...).CombinedOutput()
		},
		verify: func(ctx context.Context, store *agent.Store, rolesValue, capabilitiesValue string) error {
			roles, capabilities, err := validatedNodeRuntime(rolesValue, capabilitiesValue)
			if err != nil {
				return err
			}
			verifyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return (agent.Client{Roles: roles, Capabilities: capabilities}).Heartbeat(verifyContext, store)
		},
	}
}

func resumeSystemdAgentInstall(ctx context.Context, store *agent.Store, executable, dataDir, roles, capabilities string, replace bool, environment agentSystemdInstallEnvironment) error {
	executable, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve Agent data path: %w", err)
	}
	for {
		operation, exists, err := store.InstallOperation(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("agent: resumable installation operation is missing")
		}
		fail := func(cause error) error {
			store.RecordInstallOperationError(context.WithoutCancel(ctx), cause)
			return cause
		}
		switch operation.Phase {
		case "enrolled":
			unit := systemdAgentUnit(executable, dataDir, roles, capabilities)
			if existing, readErr := environment.readFile(environment.unitPath); readErr == nil && !strings.Contains(string(existing), "Description=Vastora Agent") {
				return fail(errors.New("refusing to replace an unrelated vastora-agent.service"))
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return fail(fmt.Errorf("inspect systemd service: %w", readErr))
			}
			temporary := environment.unitPath + ".tmp"
			if err := environment.writeFile(temporary, []byte(unit), 0o644); err != nil {
				return fail(fmt.Errorf("write systemd service: %w", err))
			}
			if err := environment.rename(temporary, environment.unitPath); err != nil {
				return fail(fmt.Errorf("install systemd service: %w", err))
			}
			if err := store.AdvanceInstallOperation(ctx, "enrolled", "unit_written"); err != nil {
				return fail(err)
			}
		case "unit_written":
			if output, err := environment.runSystemctl("daemon-reload"); err != nil {
				return fail(fmt.Errorf("reload systemd: %s: %w", strings.TrimSpace(string(output)), err))
			}
			if err := store.AdvanceInstallOperation(ctx, "unit_written", "reloaded"); err != nil {
				return fail(err)
			}
		case "reloaded":
			if output, err := environment.runSystemctl("enable", "vastora-agent.service"); err != nil {
				return fail(fmt.Errorf("enable Agent service: %s: %w", strings.TrimSpace(string(output)), err))
			}
			if err := store.AdvanceInstallOperation(ctx, "reloaded", "enabled"); err != nil {
				return fail(err)
			}
		case "enabled":
			action := "start"
			if replace {
				action = "restart"
			}
			if output, err := environment.runSystemctl(action, "vastora-agent.service"); err != nil {
				return fail(fmt.Errorf("%s Agent service: %s: %w", action, strings.TrimSpace(string(output)), err))
			}
			if err := store.AdvanceInstallOperation(ctx, "enabled", "started"); err != nil {
				return fail(err)
			}
		case "started":
			if environment.verify == nil {
				return fail(errors.New("verify Agent Center connection: verifier is required"))
			}
			if err := environment.verify(ctx, store, roles, capabilities); err != nil {
				return fail(fmt.Errorf("verify Agent heartbeat with the requested Center: %w", err))
			}
			if err := store.AdvanceInstallOperation(ctx, "started", "healthy"); err != nil {
				return fail(err)
			}
		case "healthy":
			if replace {
				return nil
			}
			return store.CompleteInstallOperation(ctx)
		case "enrollment_pending":
			return fail(errors.New("agent: enrollment must complete before systemd installation"))
		default:
			return fail(errors.New("agent: stored installation phase is invalid"))
		}
	}
}

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
