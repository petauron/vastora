package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/petauron/vastora/internal/center"
)

func runCenterSupersededHistoryPurge(arguments []string) error {
	flags := flag.NewFlagSet("center purge-superseded-glivela-history", flag.ContinueOnError)
	dir := flags.String("data-dir", "", "Center state directory")
	old := flags.String("application-id", "", "exact superseded Meridian installation")
	replacement := flags.String("replacement-id", "", "existing native Glivela installation")
	name := flags.String("confirm-name", "", "exact active Agent name")
	commit := flags.Bool("commit", false, "permanently delete obsolete history; default rolls back the preview")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *dir == "" || *old == "" || *replacement == "" || *name == "" || flags.NArg() != 0 {
		return errors.New("data directory, both installation IDs and exact Agent name are required")
	}
	store, err := center.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := store.PurgeSupersededGlivelaHistory(ctx, *old, *replacement, *name, *commit)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
