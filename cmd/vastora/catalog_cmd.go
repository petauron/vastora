package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/catalog"
)

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
