package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/catalog"
)

func runCatalog(arguments []string) error {
	return runCatalogWithIO(arguments, os.Stdout, os.Stderr)
}

func runCatalogWithIO(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("catalog command is required")
	}
	switch arguments[0] {
	case "keygen":
		flags := flag.NewFlagSet("catalog keygen", flag.ContinueOnError)
		flags.SetOutput(stderr)
		outDir := flags.String("out-dir", "", "directory for the generated Ed25519 key files")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *outDir == "" {
			return errors.New("--out-dir is required")
		}
		return catalogKeygen(*outDir, stdout)
	case "validate":
		flags := flag.NewFlagSet("catalog validate", flag.ContinueOnError)
		flags.SetOutput(stderr)
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
		fmt.Fprintf(stdout, "valid catalog: %d app(s)\n", len(parsed.Apps))
		return nil
	case "sign":
		flags := flag.NewFlagSet("catalog sign", flag.ContinueOnError)
		flags.SetOutput(stderr)
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
	case "verify":
		flags := flag.NewFlagSet("catalog verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		envelopePath := flags.String("envelope", "", "signed catalog envelope file")
		publicKeyPath := flags.String("public-key", "", "Ed25519 public key file")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if *envelopePath == "" || *publicKeyPath == "" {
			return errors.New("--envelope and --public-key are required")
		}
		return catalogVerify(*envelopePath, *publicKeyPath, stdout)
	default:
		return errors.New("unknown catalog command")
	}
}

func catalogKeygen(outDir string, stdout io.Writer) error {
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Ed25519 key: %w", err)
	}
	privatePath := filepath.Join(outDir, "catalog-signing-private.key")
	publicPath := filepath.Join(outDir, "catalog-signing-public.key")
	for _, path := range []string{privatePath, publicPath} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("create %s: file already exists", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
	}
	if err := writeNewFile(privatePath, []byte(base64.RawURLEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		return err
	}
	if err := writeNewFile(publicPath, []byte(base64.RawURLEncoding.EncodeToString(publicKey)+"\n"), 0o644); err != nil {
		return errors.Join(err, os.Remove(privatePath))
	}
	fingerprint := sha256Sum(publicKey)
	fmt.Fprintf(stdout, "private key: %s\npublic key: %s\npublic key fingerprint: %s\n", privatePath, publicPath, fingerprint)
	return nil
}

func catalogSign(inputPath, privateKeyPath, keyID, outputPath string) error {
	payload, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	privateKeyInfo, err := os.Stat(privateKeyPath)
	if err != nil {
		return fmt.Errorf("stat private key: %w", err)
	}
	if !privateKeyInfo.Mode().IsRegular() {
		return errors.New("private key must be a regular file")
	}
	if privateKeyInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("private key permissions are too broad; remove group and other access")
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

func catalogVerify(envelopePath, publicKeyPath string, stdout io.Writer) error {
	rawEnvelope, err := os.ReadFile(envelopePath)
	if err != nil {
		return fmt.Errorf("read catalog envelope: %w", err)
	}
	envelope, err := catalog.ParseEnvelope(rawEnvelope)
	if err != nil {
		return err
	}
	encodedPublicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(encodedPublicKey)))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("public key is not a valid Ed25519 key")
	}
	parsed, _, err := catalog.Verify(envelope, ed25519.PublicKey(publicKey))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "verified catalog: %d app(s), key id: %q\n", len(parsed.Apps), envelope.KeyID)
	return nil
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
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set mode on %s: %w", path, err)
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
