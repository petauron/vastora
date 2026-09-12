// catalog-check validates reviewed catalog contracts and public release assets.
// It has no signing keys, registry credentials, or publication write capability.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/catalog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	f := flag.NewFlagSet("catalog-check", flag.ContinueOnError)
	input := f.String("catalog", "catalog/catalog.json", "reviewed catalog")
	rootDirectory := f.String("root-directory", "", "optional reviewed public root chain directory (1.root.json onward)")
	artifacts := f.Bool("artifacts", false, "download and verify public native assets and OCI manifests for both supported platforms")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("catalog-check: unexpected arguments")
	}
	if *rootDirectory != "" {
		if err := catalog.ValidateOfficialRootDirectory(*rootDirectory, time.Now().UTC()); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	value, err := catalog.ParseCatalog(raw)
	if err != nil {
		return err
	}
	if err := agent.ValidateOfficialCatalog(value); err != nil {
		return err
	}
	if !*artifacts {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for _, app := range value.Apps {
		for _, artifact := range app.Artifacts {
			if err := agent.VerifyOfficialNativeArtifact(ctx, app, artifact); err != nil {
				return fmt.Errorf("catalog-check: %s %s artifact failed verification: %w", app.ID, artifact.Architecture, err)
			}
		}
		for _, image := range app.Images {
			if err := verifyImage(ctx, image.Reference); err != nil {
				return fmt.Errorf("catalog-check: %s image failed verification: %w", app.ID, err)
			}
		}
	}
	fmt.Println("Catalog contracts, native digests/platforms, and pinned OCI platforms verified.")
	return nil
}

func verifyImage(ctx context.Context, reference string) error {
	ref, err := name.NewDigest(reference)
	if err != nil {
		return err
	}
	// Explicit anonymous access: do not read the developer/runner's Docker auth.
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(authn.Anonymous)}
	descriptor, err := remote.Get(ref, options...)
	if err != nil {
		return err
	}
	hash, _, err := v1.SHA256(bytes.NewReader(descriptor.Manifest))
	if err != nil || hash.String() != ref.DigestStr() {
		return errors.New("pinned OCI manifest digest mismatch")
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		image, err := remote.Image(ref, append(options, remote.WithPlatform(v1.Platform{OS: "linux", Architecture: architecture}))...)
		if err != nil {
			return err
		}
		manifest, err := image.Manifest()
		if err != nil {
			return err
		}
		if manifest.Config.Size <= 0 || manifest.Config.Size > catalog.MaxEnvelopeBytes {
			return errors.New("OCI image configuration exceeds size limits")
		}
		config, err := image.ConfigFile()
		if err != nil {
			return err
		}
		if config.OS != "linux" || config.Architecture != architecture {
			return errors.New("OCI image platform mismatch")
		}
	}
	return nil
}
