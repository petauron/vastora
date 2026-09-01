// Command vastora provides the local control-plane, Agent, and catalog tools.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

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
	case "status", "update", "uninstall":
		return runLocalManagement(arguments)
	case "catalog":
		return runCatalog(arguments[1:])
	case "center":
		return runCenter(arguments[1:])
	case "agent":
		return runAgent(arguments[1:])
	case "deployer":
		return runDeployer(arguments[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	default:
		return usageError()
	}
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

func usageError() error {
	printUsage(os.Stderr)
	return errors.New("invalid command")
}

func printUsage(writer *os.File) {
	fmt.Fprint(writer, `Vastora control-plane tools

Usage:
  vastora version
  vastora status
  vastora update
  vastora uninstall
  vastora center serve --data-dir DIR [--agent-connect-url URL] [--headscale-allowed-url URL] [--listen 127.0.0.1:8080] [--tls-cert CERT --tls-key KEY]
  vastora center agent-token create --data-dir DIR --site-id SITE --name NAME --center-url URL [--gateway] [--tunnel] [--headscale]
  vastora center backup --data-dir DIR --output FILE --password-file FILE
  vastora center restore --input FILE --data-dir NEW_DIR --password-file FILE
  vastora deployer serve --socket /run/vastora-deployer/deployer.sock
  vastora agent init --data-dir DIR
  vastora agent enroll --data-dir DIR --center-url URL --token-file FILE [--ca-certificate FILE]
  vastora agent install --center-url URL --token-file FILE [--ca-certificate FILE] [--replace-existing]
  vastora agent status [--data-dir /var/lib/vastora/agent]
  vastora agent configure --roles worker[,gateway] --capabilities docker[,gateway,tunnel]
  vastora agent configure-center --center-url URL [--ca-certificate FILE]
  vastora agent resolve-legacy-task --task-id ID --confirm-external-state-reviewed
  vastora agent adopt-tailscale --confirm-vastora-ownership
  vastora agent update [--data-dir /var/lib/vastora/agent] [--center-url URL]
  vastora agent uninstall --purge
  vastora agent serve --data-dir DIR [--listen 127.0.0.1:8090]
  vastora catalog keygen --out-dir DIR
  vastora catalog validate --catalog FILE
  vastora catalog sign --catalog FILE --private-key FILE --key-id ID --output FILE
  vastora catalog verify --envelope FILE --public-key FILE
`)
}
