package recovery

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployer"
)

// Export only the mounted data belonging to the pinned Vastora container. A
// directory that merely resembles Headscale data is not version provenance.
func verifyBundledHeadscaleSource(ctx context.Context, dataDir, configDir string) (string, error) {
	// Remote Docker endpoints cannot prove that host-local files are the
	// inspected container's mounts, even if the path strings happen to match.
	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		return "", errors.New("recovery: cannot inspect bundled Headscale ownership")
	}
	defer docker.Close()
	inspected, err := docker.ContainerInspect(ctx, deployer.DefaultHeadscaleContainer, client.ContainerInspectOptions{})
	if err != nil {
		return "", errors.New("recovery: bundled Headscale container must remain available for version and volume verification")
	}
	current := inspected.Container
	if current.Config == nil || current.Config.Image != deployer.DefaultHeadscaleImage || current.Config.Labels["io.vastora.managed"] != "true" || current.Config.Labels["io.vastora.component"] != "center-headscale" {
		return "", errors.New("recovery: Headscale container is not the supported owned, pinned release")
	}
	for target, source := range map[string]string{"/var/lib/headscale": dataDir, "/etc/headscale": configDir} {
		expected, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", errors.New("recovery: Headscale source is unavailable")
		}
		matches := 0
		for _, mounted := range current.Mounts {
			if mounted.Destination != target {
				continue
			}
			actual, err := filepath.EvalSymlinks(mounted.Source)
			if err != nil || actual != expected {
				return "", errors.New("recovery: Headscale backup source differs from the owned container mount")
			}
			matches++
		}
		if matches != 1 {
			return "", errors.New("recovery: Headscale backup mount cannot be verified")
		}
	}
	return current.ID, nil
}
