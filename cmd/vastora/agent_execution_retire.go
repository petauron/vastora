package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

func runRetireLegacyExecution(arguments []string) error {
	if err := requireLinuxRoot("agent retire-legacy-execution"); err != nil {
		return err
	}
	flags := flag.NewFlagSet("agent retire-legacy-execution", flag.ContinueOnError)
	directory := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
	identity := flags.String("application-id", "", "exact retained legacy application identity")
	state := flags.String("native-state", "", "owned native entry state directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *identity == "" || *state == "" {
		return errors.New("application-id and native-state required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "vastora-agent.service", "--property=LoadState,ActiveState,SubState,MainPID,ControlPID").Output()
	if err != nil || !nativeStageAgentStopped(string(out)) {
		return errors.New("keep the Agent stopped during local execution retirement; do not stop Glivela")
	}
	store, err := agent.Open(*directory)
	if err != nil {
		return err
	}
	defer store.Close()
	backup, err := (agent.ApplicationExecutor{Store: store}).RetireLegacyExecution(ctx, *identity, *state)
	if err != nil {
		return err
	}
	fmt.Println("Legacy execution retired; retained recovery evidence:", backup)
	return nil
}
