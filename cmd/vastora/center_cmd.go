package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
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
		deployerSocket := flags.String("deployer-socket", "", "Unix socket for the restricted infrastructure deployment helper")
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
		if *tlsCert == "" && !loopbackAddress(*listen) {
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
		go store.RunThreeXUIInboundPlanResets(maintenanceContext, time.Minute, func(err error) {
			fmt.Fprintf(os.Stderr, "Center REALITY traffic plan reset: %v\n", err)
		})
		centerServer := center.NewServer(store, *webDir, *tlsCert != "").
			WithOfficialCatalog(catalogPayload).
			WithAgentBinaries(*agentBinariesDir).
			WithSetupAgentConnectURL(normalizedAgentConnectURL)
		if *deployerSocket != "" {
			installer, err := deployapi.NewClient(*deployerSocket)
			if err != nil {
				return err
			}
			centerServer.WithHeadscaleInstaller(installer)
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
	if password == "" {
		return "", errors.New("password file is empty")
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
