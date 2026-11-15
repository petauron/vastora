package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/vastora/internal/center"
)

func runCenterExecutionRetirement(arguments []string) error {
	flags := flag.NewFlagSet("center retire-glivela-execution", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	directory := flags.String("data-dir", "", "existing Center state directory")
	evidenceFile := flags.String("evidence-file", "", "private current operator inspection JSON")
	confirm := flags.Bool("confirm", false, "archive only the verified superseded execution record")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !*confirm || *directory == "" || *evidenceFile == "" || flags.NArg() != 0 {
		return errors.New("existing --data-dir, private --evidence-file and explicit --confirm required")
	}
	for _, name := range []string{"center.db", "center.key"} {
		info, err := os.Lstat(filepath.Join(*directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("existing Center database and original key required")
		}
	}
	info, err := os.Lstat(*evidenceFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return errors.New("bounded private operator evidence file required")
	}
	file, err := os.Open(*evidenceFile)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var evidence center.GlivelaExecutionRetirement
	if decoder.Decode(&evidence) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid execution retirement evidence")
	}
	store, err := center.Open(*directory)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.RetireGlivelaExecutionRecord(ctx, evidence); err != nil {
		return err
	}
	fmt.Println("Superseded execution record archived; runtime, services, identities and history unchanged.")
	return nil
}
