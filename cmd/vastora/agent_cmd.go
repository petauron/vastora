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
	"github.com/petauron/vastora/internal/hostservice"
	"github.com/petauron/vastora/internal/nodesecurity"
	"github.com/petauron/vastora/internal/tailscalehost"
	"github.com/sethvargo/go-retry"
)

func runAgent(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("agent command is required")
	}
	switch arguments[0] {
	case "prepare-runtime-directories", "install-runtime-directories", "uninstall-runtime-directories":
		return runRuntimeDirectories(arguments)
	case "prepare-native", "run-native", "check-native":
		flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
		path := flags.String("spec", "", "executor-owned native process specification")
		digest := flags.String("sha256", "", "installed specification digest")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *path == "" || *digest == "" {
			return errors.New("native service specification and digest required")
		}
		if arguments[0] == "prepare-native" {
			return agent.PrepareOpenRCProcess(*path, *digest)
		}
		if arguments[0] == "check-native" {
			return agent.WaitOpenRCProcess(context.Background(), *path, *digest)
		}
		return agent.RunOpenRCProcess(*path, *digest)
	case "execute-os-reinstall":
		return executeOSReinstall(arguments[1:])
	case "prepare-center-upgrade-recovery":
		return runPrepareCenterUpgradeRecovery(arguments[1:])
	case "retire-legacy-execution":
		return runRetireLegacyExecution(arguments[1:])
	case "retire-landing-execution":
		return runRetireLandingExecution(arguments[1:])
	case "stage-native-package":
		return runStageNativePackage(arguments[1:])
	case "security-watchdog":
		if len(arguments) != 1 {
			return errors.New("security-watchdog takes no arguments")
		}
		return nodesecurity.NewHost().Watchdog(context.Background())
	case "security-verify-ssh":
		if len(arguments) != 2 {
			return errors.New("usage: vastora agent security-verify-ssh REVISION")
		}
		return nodesecurity.NewHost().VerifySSH(context.Background(), arguments[1])
	case "landing-firewall":
		if len(arguments) != 1 {
			return errors.New("landing-firewall takes no arguments")
		}
		return agent.RestoreLandingFirewall(context.Background())
	case "install":
		flags := flag.NewFlagSet("agent install", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		tokenFile := flags.String("token-file", "", "0600 enrollment token file, or - for standard input")
		caFingerprint := flags.String("ca-fingerprint", "", "expected Center CA SHA-256 public-key fingerprint")
		caCertificate := flags.String("ca-certificate", "", "PEM certificate for a private Center CA")
		replaceExisting := flags.Bool("replace-existing", false, "replace an existing Center enrollment after explicit confirmation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if runtime.GOOS != "linux" {
			return errors.New("agent install supports Linux systemd and OpenRC hosts")
		}
		if os.Geteuid() != 0 {
			return errors.New("agent install must run as root")
		}
		if _, err := hostservice.Detect(); err != nil {
			return err
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
		caCertificatePEM, err := readOptionalCACertificate(*caCertificate)
		if err != nil {
			return err
		}
		var enrollment agent.Enrollment
		if *replaceExisting {
			enrollment, err = client.MigrateEnrollment(context.Background(), store, *centerURL, token, *caFingerprint, caCertificatePEM)
		} else {
			enrollment, err = client.Enroll(context.Background(), store, *centerURL, token, *caFingerprint, caCertificatePEM)
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
		if err := resumeAgentServiceInstall(context.Background(), store, executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities), *replaceExisting, defaultAgentServiceInstallEnvironment()); err != nil {
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
		promotionID := flags.String("promotion-id", "", "Center-issued gateway promotion ID; requires a stopped Agent and existing recovery point")
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
		if *promotionID != "" {
			if strings.Join(roles, ",") != "worker,gateway" || capabilities.LandingOnly || !capabilities.Docker || !capabilities.Gateway || capabilities.Tunnel {
				return errors.New("gateway promotion permits only worker,gateway with docker,gateway capabilities")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			state, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState", "--value", "vastora-agent.service").Output()
			if err != nil || strings.TrimSpace(string(state)) != "inactive" {
				return errors.New("stop the existing Agent before applying its gateway promotion")
			}
			if err := exec.CommandContext(ctx, "/usr/bin/docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
				return errors.New("install and verify the existing managed Docker prerequisite before promotion")
			}
			grant, err := (agent.Client{}).PrepareGatewayPromotion(ctx, store, *promotionID)
			if err != nil {
				return err
			}
			if err := store.CommitGatewayPromotion(ctx, grant); err != nil {
				return err
			}
		}
		if err := store.ValidateNodePurpose(context.Background(), capabilities); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		if err := installAgentService(executable, *dataDir, strings.Join(roles, ","), nodeCapabilitiesString(capabilities)); err != nil {
			return err
		}
		if *promotionID == "" {
			if err := agentServiceAction(context.Background(), "restart"); err != nil {
				return err
			}
		}
		fmt.Println("Agent purpose updated; Center will refresh it after the next heartbeat")
		return nil
	case "configure-center":
		flags := flag.NewFlagSet("agent configure-center", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
		centerURL := flags.String("center-url", "", "Center HTTPS URL or loopback HTTP URL")
		caFingerprint := flags.String("ca-fingerprint", "", "expected Center CA SHA-256 public-key fingerprint")
		caCertificate := flags.String("ca-certificate", "", "PEM certificate for a private Center CA")
		deferLoopbackHealthVerification := flags.Bool("defer-loopback-health-verification", false, "save a loopback Center URL before the co-located Center restarts")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if err := requireLinuxRoot("agent configure-center"); err != nil {
			return err
		}
		if *centerURL == "" || flags.NArg() != 0 {
			return errors.New("--center-url is required")
		}
		caCertificatePEM, err := readOptionalCACertificate(*caCertificate)
		if err != nil {
			return err
		}
		if err := configureAgentCenter(context.Background(), *dataDir, *centerURL, *caFingerprint, caCertificatePEM, *deferLoopbackHealthVerification); err != nil {
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
		caCertificate := flags.String("ca-certificate", "", "PEM certificate for a private Center CA")
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
			caCertificatePEM, readErr := readOptionalCACertificate(*caCertificate)
			if readErr != nil {
				return readErr
			}
			verified, verifyErr := (agent.Client{}).VerifyCenterURL(context.Background(), *centerURL, *caFingerprint, caCertificatePEM)
			if verifyErr != nil {
				return fmt.Errorf("verify requested Center URL: %w", verifyErr)
			}
			if verified.URL != connection.CenterURL || verified.CAFingerprint != connection.CAFingerprint || verified.CACertificatePEM != connection.CACertificatePEM {
				connection.CenterURL = verified.URL
				connection.CAFingerprint = verified.CAFingerprint
				connection.CACertificatePEM = verified.CACertificatePEM
				if err := store.ReplaceConnection(context.Background(), connection); err != nil {
					return fmt.Errorf("save requested Center URL: %w", err)
				}
			}
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate vastora executable: %w", err)
		}
		version, err := updateAgentExecutable(context.Background(), connection, executable, func() error {
			return agentServiceAction(context.Background(), "restart")
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
		if err := uninstallAgentHostLocally(context.Background(), *dataDir, *deleteData, *runtimeCleaned, *keepBinary); err != nil {
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
		if err := runPersistentHostUpdate(context.Background(), *operationFile); err != nil {
			return err
		}
		if isOpenRCHost() {
			return cleanPersistentHostUpdate(*operationFile)
		}
		return nil
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
		caCertificate := flags.String("ca-certificate", "", "PEM certificate for a private Center CA")
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
		caCertificatePEM, err := readOptionalCACertificate(*caCertificate)
		if err != nil {
			return err
		}
		enrollment, err := (agent.Client{}).Enroll(context.Background(), store, *centerURL, token, *caFingerprint, caCertificatePEM)
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
	case "check-tailscale":
		flags := flag.NewFlagSet("agent check-tailscale", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		requireRunning := flags.Bool("require-running", false, "also require a running session and loaded privacy settings")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: vastora agent check-tailscale [--require-running]")
		}
		if err := requireLinuxRoot("agent check-tailscale"); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return tailscalehost.CheckCompatibility(ctx, runHostCommand, *requireRunning)
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
		roles, capabilities, err := validatedNodeRuntime(*rolesValue, *capabilitiesValue)
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
		closeCenterSession := client.UseCenterGRPCSession()
		defer closeCenterSession()
		if err := store.ValidateNodePurpose(context.Background(), capabilities); err != nil {
			return err
		}
		client.PublicEgress = agent.NewPublicEgressObserver()
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
			if !capabilities.LandingOnly && !hostState.TailscaleEnrolled && hostState.TailscaleOwnership == "none" {
				client.OSReinstaller = osReinstallerForHost(*dataDir, executable)
			}
		}
		if !capabilities.LandingOnly {
			client.Executor = agent.ApplicationExecutor{Host: agent.SystemdHostApplicationManager{}, Store: store}
		}
		client.Capabilities.IPQuality = capabilities.Docker && runtime.GOOS == "linux"
		client.Capabilities.NetworkDiagnostics = !capabilities.LandingOnly && runtime.GOOS == "linux"
		client.Capabilities.ReturnRoute = !capabilities.LandingOnly && runtime.GOOS == "linux" && os.Geteuid() == 0
		_, iperfErr := exec.LookPath("iperf3")
		client.Capabilities.BandwidthDiagnostics = !capabilities.LandingOnly && runtime.GOOS == "linux" && iperfErr == nil
		client.Capabilities.NativeEgress = (capabilities.Docker || capabilities.LandingOnly) && runtime.GOOS == "linux" && os.Geteuid() == 0
		client.Capabilities.LandingEgressIP = runtime.GOOS == "linux" && os.Geteuid() == 0
		client.Capabilities.SenulunInspection = capabilities.Docker && runtime.GOOS == "linux"
		client.Capabilities.SenulunRotation = client.Capabilities.SenulunInspection
		client.Capabilities.SenulunReporting = client.Capabilities.SenulunInspection
		client.Capabilities.SenulunRestore = capabilities.Docker && runtime.GOOS == "linux" && os.Geteuid() == 0
		client.Capabilities.HostProfile = !capabilities.LandingOnly && runtime.GOOS == "linux"
		client.Capabilities.OSReinstallPreflight = !capabilities.LandingOnly && runtime.GOOS == "linux" && os.Geteuid() == 0
		client.Capabilities.OSReinstall = client.OSReinstaller != nil
		client.Capabilities.NodeSecurity = runtime.GOOS == "linux" && os.Geteuid() == 0
		if capabilities.Docker {
			client.NodeListener = agent.DockerLayer4Provisioner{Image: *haproxyImage}
		}
		if capabilities.Gateway {
			caddyDriver, err := agent.NewCaddyGatewayDriver(*caddyAdmin)
			if err != nil {
				return err
			}
			caddyProvisioner := agent.DockerGatewayProvisioner{Image: *caddyImage, AdminListen: caddyDriver.AdminListen, AdminSocketPath: caddyDriver.AdminSocketPath}
			caddyProvisioner.Sites = store.SharedCaddySites
			caddyDriver.SystemGateway = caddyProvisioner
			caddyDriver.SiteContainer = true
			caddyDriver.SiteRuntime = caddyProvisioner.ReconcileWebsites
			layer4 := agent.DockerLayer4Provisioner{Image: *haproxyImage}
			managedDriver := &agent.ManagedGatewayDriver{Caddy: caddyDriver, Layer4: layer4, Runtime: caddyProvisioner}
			if err := store.ConfigureManagedCaddy(managedDriver); err != nil {
				return err
			}
			client.GatewayDriver = managedDriver
			client.GatewayProvisioner = agent.ManagedGatewayProvisioner{
				Caddy:  caddyProvisioner,
				Layer4: layer4,
				Driver: managedDriver,
			}
		}
		if !capabilities.Gateway && runtime.GOOS == "linux" && os.Geteuid() == 0 {
			admin := *caddyAdmin
			if !capabilities.Docker {
				var err error
				admin, err = agent.NativeCaddyAdminURL()
				if err != nil {
					return err
				}
			}
			driver, err := agent.NewCaddyGatewayDriver(admin)
			if err != nil {
				return err
			}
			if capabilities.Docker {
				provisioner := agent.DockerGatewayProvisioner{Image: *caddyImage, AdminListen: driver.AdminListen, AdminSocketPath: driver.AdminSocketPath, Sites: store.SharedCaddySites}
				driver.SystemGateway, driver.SiteRuntime, driver.SiteContainer = provisioner, provisioner.ReconcileWebsites, true
				driver.SiteIdle = provisioner.Remove
			} else {
				provisioner := agent.NativeCaddySiteRuntime{StateDirectory: *dataDir}
				driver.SiteRuntime = provisioner.ReconcileWebsites
				driver.SiteIdle = provisioner.Idle
			}
			if err := store.ConfigureSharedCaddy(driver); err != nil {
				return err
			}
		}
		if capabilities.Tunnel {
			client.TunnelProvisioner = agent.DockerTunnelProvisioner{}
		}
		controlLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		// Upgrade existing managed installations too. This is local platform
		// setup before any Center contact, never a replay of a queued task.
		if runtime.GOOS == "linux" && os.Geteuid() == 0 {
			if unit, err := os.ReadFile(vastoraAgentUnitPath); err == nil && strings.Contains(string(unit), "Description=Vastora Agent") && strings.Contains(string(unit), " agent serve ") {
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				if err := installRuntimeDirectoriesUnit(executable); err != nil {
					return fmt.Errorf("prepare managed runtime directories: %w", err)
				}
			}
		}
		// Reconnection is observation only. A failed heartbeat must not trigger
		// offline application restoration or replay a management operation.
		go func() {
			// Restore owned local startup resources before Center contact or task
			// claims. Heartbeat/reconnect handling never replays this recovery.
			if capabilities.Docker {
				startup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				err := store.RecoverStartupRuntime(startup)
				cancel()
				if err != nil {
					controlLogger.Error("Gateway startup network recovery failed", "event", "gateway.startup_network", "error", controlplane.SafeError(err.Error()))
					go retryStartupRuntimeRecovery(context.Background(), store, controlLogger)
				}
			}
			// Glivela resumes its own proxy processes. Agent reconnection must
			// not restart retained Dante/Xray installations after handover.
			for {
				if err := client.StartupHeartbeat(context.Background(), store); err == nil {
					break
				} else {
					controlLogger.Error("Initial Agent heartbeat failed", "event", "control_plane.heartbeat", "error", controlplane.SafeError(err.Error()))
				}
				time.Sleep(time.Second)
			}
			go client.RunHeartbeats(context.Background(), store, *heartbeatInterval, func(err error) {
				controlLogger.Error("Agent heartbeat failed", "event", "control_plane.heartbeat", "error", controlplane.SafeError(err.Error()))
			})
			client.RunTasks(context.Background(), store, func(err error) {
				controlLogger.Error("Agent task channel failed", "event", "control_plane.task", "error", controlplane.SafeError(err.Error()))
			})
		}()
		fmt.Printf("Agent health listener on %s\n", *listen)
		return http.ListenAndServe(*listen, store.Handler())
	default:
		return errors.New("unknown agent command")
	}
}

func configureAgentCenter(ctx context.Context, dataDir, centerURL, caFingerprint, caCertificatePEM string, deferLoopbackHealthVerification bool) error {
	store, err := agent.Open(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	connection, err := store.Connection(ctx)
	if err != nil {
		return errors.New("agent must be enrolled before its Center can be changed")
	}
	var verified agent.VerifiedCenter
	if deferLoopbackHealthVerification {
		if strings.TrimSpace(caFingerprint) != "" || strings.TrimSpace(caCertificatePEM) != "" {
			return errors.New("deferred Center health verification does not accept CA options")
		}
		verified.URL, err = agent.NormalizeDeferredLoopbackCenterURL(centerURL)
		if err != nil {
			return err
		}
	} else {
		verified, err = (agent.Client{}).VerifyCenterURL(ctx, centerURL, caFingerprint, caCertificatePEM)
		if err != nil {
			return fmt.Errorf("verify requested Center URL: %w", err)
		}
	}
	if verified.URL == connection.CenterURL && verified.CAFingerprint == connection.CAFingerprint && verified.CACertificatePEM == connection.CACertificatePEM {
		if agentLoopbackCenterURL(verified.URL) {
			return store.SetLocalCenterChannel(verified.URL)
		}
		return store.SetLocalCenterChannel("")
	}
	previous := connection
	connection.CenterURL = verified.URL
	connection.CAFingerprint = verified.CAFingerprint
	connection.CACertificatePEM = verified.CACertificatePEM
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
	if capabilities.LandingOnly && (len(roles) != 1 || roles[0] != "worker") {
		return nil, agent.Capabilities{}, errors.New("landing-only nodes require only the worker role")
	}
	return roles, capabilities, nil
}

func updateAgentExecutable(ctx context.Context, connection agent.Connection, executable string, restart func() error) (string, error) {
	executable, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve Agent executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agent executable is not a regular file")
	}
	temporaryPath, expectedVersion, err := downloadAgentUpdateCandidate(ctx, connection, filepath.Dir(executable), agent.OpenAgentBinary)
	if err != nil {
		return "", err
	}
	defer os.Remove(temporaryPath)
	backupPath := executable + ".previous"
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Keep the running executable in place until the verified candidate can
	// replace it atomically. A failed update never automatically downgrades it.
	if err := copyExecutableAtomic(executable, backupPath); err != nil {
		return "", fmt.Errorf("preserve previous Agent binary: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		return "", fmt.Errorf("install Agent update: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := restart(); err != nil {
		return "", fmt.Errorf("restart updated Agent; stopped for explicit maintenance, previous binary preserved: %w", err)
	}
	return expectedVersion, nil
}

// Allow slow links to download a bounded candidate without relaxing TLS,
// integrity, version, cancellation, or retry checks.
const agentUpdateDownloadTimeout = 30 * time.Minute

type agentUpdateDownloadSource func(context.Context, agent.Connection, string, string) (io.ReadCloser, string, string, int64, error)

func downloadAgentUpdateCandidate(ctx context.Context, connection agent.Connection, directory string, open agentUpdateDownloadSource) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, agentUpdateDownloadTimeout)
	defer cancel()
	var candidate, version string
	err := retry.Do(ctx, retry.WithMaxRetries(3, retry.NewExponential(2*time.Second)), func(ctx context.Context) error {
		var err error
		candidate, version, err = downloadAgentUpdateAttempt(ctx, connection, directory, open)
		return err
	})
	return candidate, version, err
}

// Each attempt creates a new request and temporary file. Partial downloads are
// removed before retrying; metadata, digest and executable checks stay mandatory.
func downloadAgentUpdateAttempt(ctx context.Context, connection agent.Connection, directory string, open agentUpdateDownloadSource) (string, string, error) {
	reader, expectedVersion, expectedDigest, downloadBytes, err := open(ctx, connection, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", retryAgentDownloadError(fmt.Errorf("download Agent update: %w", err))
	}
	defer reader.Close()
	expectedVersion = strings.TrimSpace(expectedVersion)
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if expectedVersion == "" || len(expectedDigest) != sha256.Size*2 {
		return "", "", errors.New("center returned incomplete Agent update metadata")
	}
	if err := requireAgentUpdateSpace(directory, downloadBytes+16<<20); err != nil {
		return "", "", err
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
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(reader, (256<<20)+1))
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
		return "", "", retryAgentDownloadError(fmt.Errorf("store Agent update: %w", copyErr))
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

const vastoraAgentUnitPath = "/etc/systemd/system/vastora-agent.service"

type agentServiceInstallEnvironment struct {
	kind       hostservice.Kind
	unitPath   string
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte, os.FileMode) error
	rename     func(string, string) error
	runService func(...string) ([]byte, error)
	verify     func(context.Context, *agent.Store, string, string) error
}

func defaultAgentServiceInstallEnvironment() agentServiceInstallEnvironment {
	kind, detectErr := hostservice.Detect()
	path, _ := hostservice.Path(kind, "vastora-agent.service")
	return agentServiceInstallEnvironment{
		kind: kind, unitPath: path, readFile: os.ReadFile, writeFile: os.WriteFile, rename: os.Rename,
		runService: func(arguments ...string) ([]byte, error) {
			if detectErr != nil {
				return nil, detectErr
			}
			unit := ""
			if len(arguments) > 1 {
				unit = arguments[1]
			}
			return nil, hostservice.Action(context.Background(), kind, nil, arguments[0], unit)
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

func resumeAgentServiceInstall(ctx context.Context, store *agent.Store, executable, dataDir, roles, capabilities string, replace bool, environment agentServiceInstallEnvironment) error {
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
			mode := os.FileMode(0644)
			marker := "Description=Vastora Agent"
			if environment.kind == hostservice.OpenRC {
				unit = openRCAgentService(executable, dataDir, roles, capabilities)
				mode = 0755
				marker = "# Managed by Vastora Agent\n"
			}
			if existing, readErr := environment.readFile(environment.unitPath); readErr == nil && !strings.Contains(string(existing), marker) {
				return fail(errors.New("refusing to replace an unrelated vastora-agent.service"))
			} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return fail(fmt.Errorf("inspect systemd service: %w", readErr))
			}
			temporary := environment.unitPath + ".tmp"
			if err := environment.writeFile(temporary, []byte(unit), mode); err != nil {
				return fail(fmt.Errorf("write systemd service: %w", err))
			}
			if err := environment.rename(temporary, environment.unitPath); err != nil {
				return fail(fmt.Errorf("install systemd service: %w", err))
			}
			if err := store.AdvanceInstallOperation(ctx, "enrolled", "unit_written"); err != nil {
				return fail(err)
			}
		case "unit_written":
			if output, err := environment.runService("reload"); err != nil {
				return fail(fmt.Errorf("reload systemd: %s: %w", strings.TrimSpace(string(output)), err))
			}
			if err := store.AdvanceInstallOperation(ctx, "unit_written", "reloaded"); err != nil {
				return fail(err)
			}
		case "reloaded":
			if output, err := environment.runService("enable", "vastora-agent.service"); err != nil {
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
			if output, err := environment.runService(action, "vastora-agent.service"); err != nil {
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
			return fail(errors.New("agent: enrollment must complete before service installation"))
		default:
			return fail(errors.New("agent: stored installation phase is invalid"))
		}
	}
}

func installAgentService(executable, dataDir, roles, capabilities string) error {
	env := defaultAgentServiceInstallEnvironment()
	if env.kind == "" {
		return errors.New("agent: supported init system required")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	unit, marker, mode := systemdAgentUnit(executable, dataDir, roles, capabilities), "Description=Vastora Agent", os.FileMode(0644)
	if env.kind == hostservice.OpenRC {
		unit = openRCAgentService(executable, dataDir, roles, capabilities)
		marker = "# Managed by Vastora Agent\n"
		mode = 0755
	}
	if existing, e := os.ReadFile(env.unitPath); e == nil && !strings.Contains(string(existing), marker) {
		return errors.New("refusing to replace an unrelated Agent service")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err := writeRootFileAtomic(env.unitPath, []byte(unit), mode); err != nil {
		return err
	}
	for _, action := range []string{"reload", "enable", "start"} {
		if _, err := env.runService(action, "vastora-agent.service"); err != nil {
			return err
		}
	}
	return nil
}

func openRCAgentService(executable, dataDir, roles, capabilities string) string {
	args := []string{"agent", "serve", "--data-dir", dataDir, "--roles", roles, "--capabilities", capabilities}
	for i := range args {
		args[i] = hostservice.Quote(args[i])
	}
	return "#!/sbin/openrc-run\n# Managed by Vastora Agent\n" +
		"description='Vastora Agent'\nsupervisor=supervise-daemon\ncommand=" + hostservice.Quote(executable) + "\ncommand_args=" + hostservice.Quote(strings.Join(args, " ")) + "\n" +
		"respawn_delay=5\nrespawn_max=0\nretry='TERM/30/KILL/5'\numask=0077\n" +
		"output_logger='/usr/bin/logger -t vastora-agent'\nerror_logger='/usr/bin/logger -t vastora-agent'\n" +
		"depend() { need net; use tailscale docker; }\n" +
		"start_pre() { " + hostservice.Quote(executable) + " agent prepare-runtime-directories; }\n"
}

func systemdAgentUnit(executable, dataDir, roles, capabilities string) string {
	after := "network-online.target docker.service"
	if capabilities == "landing" {
		after = "network-online.target tailscaled.service"
	}
	return `[Unit]
Description=Vastora Agent
After=` + after + `
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + systemdQuote(executable) + ` agent serve --data-dir ` + systemdQuote(dataDir) + ` --roles ` + systemdQuote(roles) + ` --capabilities ` + systemdQuote(capabilities) + `
Restart=always
RestartSec=5s
TimeoutStopSec=30s
LogRateLimitIntervalSec=30s
LogRateLimitBurst=100

[Install]
WantedBy=multi-user.target
`
}

func systemdQuote(value string) string { return strconv.Quote(value) }

func nodeCapabilitiesString(value agent.Capabilities) string {
	capabilities := make([]string, 0, 3)
	if value.LandingOnly {
		capabilities = append(capabilities, "landing")
	}
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
		case "landing":
			capabilities.LandingOnly = true
			capabilities.NativeEgress = true
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
	if capabilities.LandingOnly && (capabilities.Docker || capabilities.Gateway || capabilities.Tunnel) {
		return agent.Capabilities{}, errors.New("landing-only capability cannot be combined with Docker, gateway or tunnel")
	}
	if !capabilities.Docker && !capabilities.Gateway && !capabilities.Tunnel && !capabilities.LandingOnly {
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

func readOptionalCACertificate(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read Center CA certificate: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return "", errors.New("center CA certificate must be a regular PEM file no larger than 1 MiB")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Center CA certificate: %w", err)
	}
	return strings.TrimSpace(string(content)), nil
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
