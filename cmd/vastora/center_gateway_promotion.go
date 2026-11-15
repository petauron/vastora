package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/vastora/internal/center"
)

func runCenterGatewayPromotion(arguments []string) error {
	flags := flag.NewFlagSet("center authorize-gateway-promotion", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	directory := flags.String("data-dir", "", "existing Center state directory")
	agentID := flags.String("agent-id", "", "exact existing landing-only Agent ID")
	confirm := flags.Bool("confirm", false, "authorize this existing node to become a worker/gateway")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !*confirm || *directory == "" || *agentID == "" || flags.NArg() != 0 {
		return errors.New("existing --data-dir, --agent-id and explicit --confirm are required")
	}
	for _, name := range []string{"center.db", "center.key"} {
		info, err := os.Lstat(filepath.Join(*directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("existing Center database and original key required")
		}
	}
	store, err := center.Open(*directory)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	grant, err := store.AuthorizeGatewayPromotion(ctx, *agentID)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(grant)
}
