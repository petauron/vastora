package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/center"
	"github.com/petauron/vastora/internal/deployapi"
)

func runCenter(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("center command is required")
	}
	switch arguments[0] {
	case "capabilities":
		if len(arguments) != 1 {
			return errors.New("center capabilities does not accept arguments")
		}
		fmt.Println("decommission-applications")
		fmt.Println("agent-host-decommission")
		return nil
	case "offline-agent-cleanups":
		flags := flag.NewFlagSet("center offline-agent-cleanups", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		deferredAgentID := flags.String("deferred-agent-id", "", "Agent on the Center host, cleaned locally last")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" || flags.NArg() != 0 {
			return errors.New("--data-dir is required")
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		cleanups, err := store.OfflineAgentCleanups(context.Background(), strings.TrimSpace(*deferredAgentID))
		if err != nil {
			return err
		}
		for _, cleanup := range cleanups {
			fmt.Printf("%s (%s)\n  %s\n", cleanup.Name, cleanup.ID, cleanup.Command)
		}
		return nil
	case "decommission-applications":
		flags := flag.NewFlagSet("center decommission-applications", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		deleteData := flags.Bool("delete-data", false, "permanently delete application data")
		forceOffline := flags.Bool("force-offline", false, "continue after explicitly acknowledging unreachable Agents")
		deferredAgentID := flags.String("deferred-agent-id", "", "Agent on the Center host, cleaned locally last")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected decommission argument")
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := store.DecommissionApplications(ctx, *deleteData, *forceOffline, strings.TrimSpace(*deferredAgentID), func(message string) {
			fmt.Println(message)
		}); err != nil {
			return err
		}
		fmt.Println("All managed applications were removed.")
		return nil
	case "agent-token":
		if len(arguments) < 2 || arguments[1] != "create" {
			return errors.New("center agent-token create is required")
		}
		flags := flag.NewFlagSet("center agent-token create", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Center state directory")
		siteID := flags.String("site-id", "", "target Site ID")
		name := flags.String("name", "", "Agent display name")
		centerURL := flags.String("center-url", "", "Agent-reachable Center URL")
		gateway := flags.Bool("gateway", false, "allow the Agent to provide service access")
		tunnel := flags.Bool("tunnel", false, "allow the Agent to run Cloudflare Tunnel")
		headscale := flags.Bool("headscale", false, "join the configured Headscale network before enrollment")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if *dataDir == "" || *siteID == "" || *name == "" || *centerURL == "" {
			return errors.New("--data-dir, --site-id, --name, and --center-url are required")
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		enrollment, err := store.CreateAgentEnrollment(context.Background(), center.AgentEnrollmentSpec{SiteID: *siteID, Name: *name, CenterURL: *centerURL, Gateway: *gateway, Tunnel: *tunnel, UseHeadscale: *headscale})
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
		coLocatedAgentURL := flags.String("co-located-agent-url", "", "host-only Center URL for a co-located Agent")
		hostNetworkAddresses := flags.String("host-network-addresses", "", "comma-separated host interface=IPv4 values supplied by the container installer")
		deployerSocket := flags.String("deployer-socket", "", "Unix socket for the restricted infrastructure deployment helper")
		allowContainerHTTP := flags.Bool("allow-container-http", false, "allow the official bridge-network container listener")
		releaseMetadataURL := flags.String("release-metadata-url", "", "HTTPS endpoint exposing immutable release metadata")
		releaseInstallerBaseURL := flags.String("release-installer-base-url", "", "HTTPS base URL containing immutable versioned installers")
		publicHelperOrigin := flags.String("public-helper-origin", "", "HTTPS origin providing public-address and public-entry verification")
		regionLookupURL := flags.String("region-lookup-url", "", "HTTPS base URL providing public-IP region lookup")
		cloudflareOAuthClientID := flags.String("cloudflare-oauth-client-id", "", "Cloudflare OAuth application client ID")
		cloudflareOAuthRedirectURL := flags.String("cloudflare-oauth-redirect-url", "", "HTTPS Cloudflare OAuth callback URL")
		cloudflareOAuthRelayURL := flags.String("cloudflare-oauth-relay-url", "", "HTTPS Cloudflare OAuth callback relay URL")
		allowPrivateHelpers := flags.Bool("allow-private-helper-endpoints", false, "allow explicitly configured HTTPS helper hostnames to resolve to private addresses")
		var headscaleAllowedURLs stringListFlag
		flags.Var(&headscaleAllowedURLs, "headscale-allowed-url", "authorized Headscale control-plane URL (repeat for multiple URLs)")
		tlsCert := flags.String("tls-cert", "", "PEM certificate path")
		tlsKey := flags.String("tls-key", "", "PEM private key path")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		if (*tlsCert == "") != (*tlsKey == "") {
			return errors.New("--tls-cert and --tls-key must be provided together")
		}
		normalizedAgentConnectURL := ""
		if *agentConnectURL != "" {
			var err error
			normalizedAgentConnectURL, err = center.NormalizeAgentConnectURL(*agentConnectURL)
			if err != nil {
				return err
			}
		}
		normalizedCoLocatedAgentURL := ""
		if *coLocatedAgentURL != "" {
			var normalizeErr error
			normalizedCoLocatedAgentURL, normalizeErr = center.NormalizeAgentConnectURL(*coLocatedAgentURL)
			if normalizeErr != nil {
				return fmt.Errorf("co-located Agent URL: %w", normalizeErr)
			}
			parsed, _ := url.Parse(normalizedCoLocatedAgentURL)
			address := net.ParseIP(parsed.Hostname())
			if parsed.Scheme != "http" || address == nil || !address.IsLoopback() {
				return errors.New("co-located Agent URL must be a loopback HTTP origin")
			}
		}
		if *tlsCert == "" && !loopbackAddress(*listen) && !*allowContainerHTTP {
			return errors.New("refusing a non-loopback HTTP listener; provide TLS certificate and key")
		}
		catalogPayload, err := os.ReadFile(*officialCatalog)
		if err != nil {
			return fmt.Errorf("read official catalog: %w", err)
		}
		store, err := center.Open(*dataDir, headscaleAllowedURLs...)
		if err != nil {
			return err
		}
		defer store.Close()
		helperRuntime, err := store.ConfigureExternalHelpers(center.ExternalHelperConfig{
			ReleaseMetadataURL:         *releaseMetadataURL,
			ReleaseInstallerBaseURL:    *releaseInstallerBaseURL,
			PublicHelperOrigin:         *publicHelperOrigin,
			RegionLookupURL:            *regionLookupURL,
			CloudflareOAuthClientID:    *cloudflareOAuthClientID,
			CloudflareOAuthRedirectURL: *cloudflareOAuthRedirectURL,
			CloudflareOAuthRelayURL:    *cloudflareOAuthRelayURL,
			AllowPrivate:               *allowPrivateHelpers,
		})
		if err != nil {
			return err
		}
		if err := store.UseHostNetworkAddresses(*hostNetworkAddresses); err != nil {
			return err
		}
		if err := store.SeedOfficialCatalog(context.Background(), catalogPayload); err != nil {
			return err
		}
		maintenanceContext, stopMaintenance := context.WithCancel(context.Background())
		defer stopMaintenance()
		if err := store.StartPublicationVerifications(maintenanceContext); err != nil {
			return fmt.Errorf("resume public entry verification: %w", err)
		}
		go store.RunPublicationCleanup(maintenanceContext, time.Minute, func(err error) {
			fmt.Fprintf(os.Stderr, "Center publication cleanup: %v\n", err)
		})
		go store.RunCertificateRenewal(maintenanceContext, 12*time.Hour, func(err error) {
			fmt.Fprintf(os.Stderr, "Center private HTTPS renewal: %v\n", err)
		})
		go store.RunRealityNameReconciliation(maintenanceContext, time.Minute, func(err error) {
			fmt.Fprintf(os.Stderr, "Center REALITY name reconciliation: %v\n", err)
		})
		go store.RunRealityGuardRevalidation(maintenanceContext, 6*time.Hour, func(err error) {
			fmt.Fprintf(os.Stderr, "Center REALITY guard revalidation: %v\n", err)
		})
		go store.RunThreeXUIInboundPlanResets(maintenanceContext, time.Minute, func(err error) {
			fmt.Fprintf(os.Stderr, "Center REALITY traffic plan reset: %v\n", err)
		})
		centerServer := center.NewServer(store, *webDir, *tlsCert != "").
			WithOfficialCatalog(catalogPayload).
			WithAgentBinaries(*agentBinariesDir).
			WithSetupAgentConnectURL(normalizedAgentConnectURL).
			WithCoLocatedAgentURL(normalizedCoLocatedAgentURL).
			WithCenterReleaseChecker(helperRuntime.ReleaseChecker).
			WithReleaseInstallerBaseURL(helperRuntime.ReleaseInstallerBaseURL).
			WithReleaseInstallerResolver(helperRuntime.ResolveReleaseInstaller).
			WithPublicAddressLookupURL(helperRuntime.PublicAddressLookupURL, helperRuntime.PublicHelperAllowPrivate)
		go centerServer.RunCatalogRefresh(maintenanceContext, time.Minute, func(err error) {
			fmt.Fprintf(os.Stderr, "Center catalog refresh: %v\n", err)
		})
		if *deployerSocket != "" {
			installer, err := deployapi.NewClient(*deployerSocket)
			if err != nil {
				return err
			}
			centerServer.WithInfrastructureManager(installer)
			centerServer.WithCenterUpdater(installer)
			go centerServer.RunSystemEndpointAliasMaintenance(maintenanceContext, time.Minute, func(err error) {
				fmt.Fprintf(os.Stderr, "Center system endpoint alias maintenance: %v\n", err)
			})
			go centerServer.RunHeadscaleAPIKeyMaintenance(maintenanceContext, 12*time.Hour, func(err error) {
				fmt.Fprintf(os.Stderr, "Center Headscale API key maintenance: %v\n", err)
			})
			go func() {
				reconcileContext, cancel := context.WithTimeout(maintenanceContext, 8*time.Minute)
				defer cancel()
				if err := centerServer.ReconcileBuiltinHeadscale(reconcileContext); err != nil {
					fmt.Fprintf(os.Stderr, "Center built-in Headscale reconciliation: %v\n", err)
				}
			}()
		}
		handler := centerServer.Handler()
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
	if err := center.ValidateBackupPassword(password); err != nil {
		return "", err
	}
	return password, nil
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}
