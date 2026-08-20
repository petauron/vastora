package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/petauron/vastora/internal/deployer"
)

func runDeployer(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "serve" {
		return errors.New("deployer serve is required")
	}
	flags := flag.NewFlagSet("deployer serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socket := flags.String("socket", "/run/vastora-deployer/deployer.sock", "Unix socket exposed to Center")
	dockerSocket := flags.String("docker-socket", "unix:///var/run/docker.sock", "Docker Engine socket")
	configDir := flags.String("headscale-config-dir", "/var/lib/vastora-headscale-config", "persisted generated Headscale configuration")
	centerOrigin := flags.String("center-origin", "127.0.0.1:8080", "loopback Center origin used by the HTTPS gateway")
	centerUID := flags.Int("center-uid", 65532, "Center user ID allowed to connect")
	centerGID := flags.Int("center-gid", 65532, "Center group ID allowed to connect")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("deployer serve does not accept positional arguments")
	}
	installer := deployer.DockerHeadscaleInstaller{
		Socket:       *dockerSocket,
		ConfigDir:    *configDir,
		CenterOrigin: *centerOrigin,
	}
	fmt.Printf("Deployment helper listening on %s\n", *socket)
	return deployer.ServeUnix(*socket, *centerUID, *centerGID, deployer.NewServer(installer).Handler())
}
