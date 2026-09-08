package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/vastora/internal/center"
	"github.com/petauron/vastora/internal/recovery"
)

func runRecovery(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("recovery command is required: status, export-agent, export-headscale, inspect, register, register-application, restore")
	}
	flags := flag.NewFlagSet("recovery "+arguments[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dataDir := flags.String("data-dir", "", "component directory, or Center directory for status/register")
	configDir := flags.String("config-dir", "", "bundled Headscale configuration volume directory")
	tailscaleState := flags.String("tailscale-state", "", "original Tailscale state file for an enrolled Agent")
	output := flags.String("output", "", "new absolute encrypted backup path")
	input := flags.String("input", "", "encrypted backup, or external application artifact")
	passwordFile := flags.String("password-file", "", "0600 backup password file")
	kind := flags.String("kind", "", "center, agent or headscale for registration")
	identity := flags.String("identity-sha256", "", "identity fingerprint obtained independently on the original host")
	id := flags.String("component-id", "", "expected component identity for restore")
	evidenceFile := flags.String("evidence", "", "operator's external backup evidence JSON")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected recovery argument")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if arguments[0] == "status" {
		if !filepath.IsAbs(*dataDir) {
			return errors.New("--data-dir must be an absolute Center directory")
		}
		view, err := center.ReadRecoveryReadiness(ctx, *dataDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(view)
	}
	if arguments[0] == "register-application" {
		if !filepath.IsAbs(*dataDir) || *input == "" || *evidenceFile == "" {
			return errors.New("--data-dir, --input and --evidence are required")
		}
		encoded, err := recovery.ReadRegularFile(*evidenceFile, 32<<10)
		if err != nil {
			return err
		}
		var evidence center.ExternalRecoveryEvidence
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&evidence) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("invalid external backup evidence")
		}
		digest, err := recoveryFileDigest(*input)
		if err != nil {
			return err
		}
		store, err := center.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.RegisterExternalRecoveryEvidence(ctx, evidence, digest); err != nil {
			return err
		}
		fmt.Println("Application backup evidence recorded. The restore result is operator-attested.")
		return nil
	}
	if *passwordFile == "" {
		return errors.New("--password-file is required")
	}
	password, err := readPrivatePassword(*passwordFile)
	if err != nil {
		return err
	}
	var artifact recovery.Artifact
	switch arguments[0] {
	case "export-agent":
		artifact, err = recovery.ExportAgent(ctx, *dataDir, *tailscaleState, center.Version, *output, password)
	case "export-headscale":
		artifact, err = recovery.ExportHeadscale(ctx, *dataDir, *configDir, center.Version, *output, password)
	case "inspect":
		artifact, err = recovery.Inspect(ctx, *input, password, center.Version)
	case "restore":
		artifact, err = recovery.Restore(ctx, *input, *dataDir, password, center.Version, *id, *identity)
	case "register":
		if !filepath.IsAbs(*dataDir) || *input == "" {
			return errors.New("--data-dir and --input are required")
		}
		store, openErr := center.Open(*dataDir)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		if err := store.RegisterRecoveryArtifact(ctx, *kind, *input, password, *identity); err != nil {
			return err
		}
		fmt.Println("Recovery evidence authenticated and recorded. Keep the encrypted artifact off-host.")
		return nil
	default:
		return errors.New("unknown recovery command")
	}
	if err != nil {
		return err
	}
	// This is the audit receipt: no password, private key, credential, or
	// decrypted member is printed. Save it independently of the source host.
	return json.NewEncoder(os.Stdout).Encode(artifact)
}

func recoveryFileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return "", errors.New("backup artifact is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open backup artifact")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("backup artifact changed while opening")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size != info.Size() {
		return "", errors.New("backup artifact changed while reading")
	}
	after, err := file.Stat()
	if err != nil || !after.ModTime().Equal(info.ModTime()) || after.Size() != size {
		return "", errors.New("backup artifact changed while reading")
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}
