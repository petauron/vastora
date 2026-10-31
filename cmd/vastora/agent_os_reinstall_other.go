//go:build !linux

package main

import "errors"
import "github.com/petauron/vastora/internal/agent"

func osReinstallerForHost(string, string) agent.HostOSReinstaller { return nil }
func executeOSReinstall([]string) error                           { return errors.New("agent: OS reinstall is unavailable") }
