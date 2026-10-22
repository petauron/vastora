package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"
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

func runRetireLandingExecution(arguments []string) error {
	if err := requireLinuxRoot("agent retire-landing-execution"); err != nil {
		return err
	}
	flags := flag.NewFlagSet("agent retire-landing-execution", flag.ContinueOnError)
	directory := flags.String("data-dir", "/var/lib/vastora/agent", "Agent state directory")
	state := flags.String("native-state", "", "owned native landing state directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *state == "" {
		return errors.New("native-state required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "vastora-agent.service", "--property=LoadState,ActiveState,SubState,MainPID,ControlPID").Output()
	if err != nil || !strings.Contains(string(out), "LoadState=masked\n") || !nativeStageAgentStopped(strings.Replace(string(out), "LoadState=masked\n", "LoadState=loaded\n", 1)) {
		return errors.New("keep Agent stopped during landing retirement; keep Glivela running")
	}
	store, err := agent.Open(*directory)
	if err != nil {
		return err
	}
	defer store.Close()
	backup, err := (agent.ApplicationExecutor{Store: store}).RetireLandingExecution(ctx, *state)
	if err != nil {
		return err
	}
	fmt.Println("Legacy landing execution retired; retained recovery evidence:", backup)
	return nil
}
