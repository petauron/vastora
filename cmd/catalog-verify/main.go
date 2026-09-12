// catalog-verify verifies a published repository from an independent trust root.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

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
	f := flag.NewFlagSet("catalog-verify", flag.ContinueOnError)
	origin := f.String("origin", "", "published TUF metadata HTTPS URL")
	directory := f.String("directory", "", "staged repository to verify without network access (instead of origin)")
	rootPath := f.String("root", "", "independently trusted root JSON")
	channel := f.String("channel", "stable", "expected channel")
	revision := f.Uint64("revision", 0, "exact expected published revision")
	hash := f.String("sha256", "", "expected catalog target SHA256 from protected publication ledger")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *rootPath == "" || (*origin == "") == (*directory == "") || *revision == 0 || *hash == "" {
		return errors.New("catalog-verify: exactly one of origin/directory, root, revision and sha256 are required")
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var result catalog.OfficialFetchResult
	if *directory != "" {
		result, err = catalog.VerifyOfficialRepository(ctx, *directory, *channel, root)
	} else {
		result, err = catalog.FetchOfficial(ctx, *origin, *channel, root, catalog.OfficialFetchState{})
	}
	if err != nil {
		return err
	}
	if result.State.Acceptance.Revision != *revision || result.State.Acceptance.SHA256 != *hash {
		return errors.New("catalog-verify: published revision or target hash does not match the approved release")
	}
	if err := agent.ValidateOfficialCatalog(result.Catalog); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result.State.Acceptance)
}
