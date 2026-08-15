// Command vastora provides the local control-plane, Node, and catalog tools.
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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/master"
	"github.com/petauron/vastora/internal/node"
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
		fmt.Println(master.Version)
		return nil
	case "catalog":
		return runCatalog(arguments[1:])
	case "master":
		return runMaster(arguments[1:])
	case "node":
		return runNode(arguments[1:])
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

func runMaster(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("master command is required")
	}
	switch arguments[0] {
	case "init":
		flags := flag.NewFlagSet("master init", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Master state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		store, err := master.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		token, err := store.Initialize(context.Background())
		if err != nil {
			return err
		}
		fmt.Println("Master initialized. Bootstrap token (shown once):")
		fmt.Println(token)
		return nil
	case "serve":
		flags := flag.NewFlagSet("master serve", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Master state directory")
		listen := flags.String("listen", "127.0.0.1:8080", "listen address")
		webDir := flags.String("web-dir", "web/dist", "compiled React web directory")
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
		if *tlsCert == "" && !loopbackAddress(*listen) {
			return errors.New("refusing a non-loopback HTTP listener; provide TLS certificate and key")
		}
		store, err := master.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		server := &http.Server{Addr: *listen, Handler: master.NewServer(store, *webDir, *tlsCert != "").Handler(), ReadHeaderTimeout: 5 * time.Second}
		fmt.Printf("Master listening on %s\n", *listen)
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
		flags := flag.NewFlagSet("master backup", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Master state directory")
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
		store, err := master.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Backup(context.Background(), *output, password); err != nil {
			return err
		}
		fmt.Printf("Encrypted Master backup written to %s\n", *output)
		return nil
	case "restore":
		flags := flag.NewFlagSet("master restore", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		input := flags.String("input", "", "encrypted backup file")
		dataDir := flags.String("data-dir", "", "new empty Master state directory")
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
		if err := master.Restore(*input, *dataDir, password); err != nil {
			return err
		}
		fmt.Printf("Master backup restored to %s\n", *dataDir)
		return nil
	default:
		return errors.New("unknown master command")
	}
}

func runNode(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("node command is required")
	}
	switch arguments[0] {
	case "init":
		flags := flag.NewFlagSet("node init", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Node state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		store, err := node.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		fmt.Printf("Node state initialized at %s\n", *dataDir)
		return nil
	case "serve":
		flags := flag.NewFlagSet("node serve", flag.ContinueOnError)
		flags.SetOutput(os.Stderr)
		dataDir := flags.String("data-dir", "", "Node state directory")
		listen := flags.String("listen", "127.0.0.1:8090", "loopback health listener")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *dataDir == "" {
			return errors.New("--data-dir is required")
		}
		if !loopbackAddress(*listen) {
			return errors.New("node health listener must be loopback-only")
		}
		store, err := node.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		fmt.Printf("Node health listener on %s\n", *listen)
		return http.ListenAndServe(*listen, store.Handler())
	default:
		return errors.New("unknown node command")
	}
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

func usageError() error {
	printUsage(os.Stderr)
	return errors.New("invalid command")
}

func printUsage(writer *os.File) {
	fmt.Fprint(writer, `Vastora control-plane tools

Usage:
  vastora version
  vastora master init --data-dir DIR
  vastora master serve --data-dir DIR [--listen 127.0.0.1:8080] [--tls-cert CERT --tls-key KEY]
  vastora master backup --data-dir DIR --output FILE --password-file FILE
  vastora master restore --input FILE --data-dir NEW_DIR --password-file FILE
  vastora node init --data-dir DIR
  vastora node serve --data-dir DIR [--listen 127.0.0.1:8090]
  vastora catalog keygen --out-dir DIR
  vastora catalog validate --catalog FILE
  vastora catalog sign --catalog FILE --private-key FILE --key-id ID --output FILE
`)
}
