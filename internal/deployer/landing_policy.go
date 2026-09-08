package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/landing"
)

const landingPolicyJournal = ".vastora-landing-policy.json"

func readLandingPolicyJournal(directory string) (*deployapi.LandingPolicyRequest, error) {
	path := filepath.Join(directory, landingPolicyJournal)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 65536 {
		return nil, errors.New("deployer: invalid landing policy ownership")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state deployapi.LandingPolicyRequest
	if json.Unmarshal(data, &state) != nil || state.Revision == 0 {
		return nil, errors.New("deployer: invalid landing policy journal")
	}
	if _, err := landing.NormalizeAccessRules(state.Rules); err != nil {
		return nil, err
	}
	return &state, nil
}

func (installer DockerHeadscaleInstaller) ApplyLandingPolicy(ctx context.Context, input deployapi.LandingPolicyRequest) error {
	if input.Revision == 0 {
		return errors.New("deployer: missing landing policy revision")
	}
	rules, err := landing.NormalizeAccessRules(input.Rules)
	if err != nil {
		return err
	}
	input.Rules = rules
	settings, err := installer.apiKeyRotationSettings()
	if err != nil {
		return err
	}
	previous, err := readLandingPolicyJournal(settings.ConfigDir)
	if err != nil {
		return err
	}
	if previous != nil {
		if previous.Revision > input.Revision {
			return errors.New("deployer: stale landing policy revision")
		}
		if previous.Revision == input.Revision {
			a, _ := json.Marshal(previous.Rules)
			b, _ := json.Marshal(input.Rules)
			if string(a) != string(b) {
				return errors.New("deployer: landing policy revision changed")
			}
		}
	}
	policy, err := landing.ExtendHeadscalePolicy(renderHeadscalePolicy(), rules)
	if err != nil {
		return err
	}
	docker, err := client.New(client.WithHost(settings.Socket))
	if err != nil {
		return err
	}
	defer docker.Close()
	current, err := inspectManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale")
	if err != nil {
		return err
	}
	if current == nil || current.Container.State == nil || !current.Container.State.Running {
		return errors.New("deployer: managed Headscale is unavailable")
	}
	// The candidate lives in the existing config volume, visible read-only to
	// Headscale. No image pulls, container replacement, or shell interpolation.
	if err := writeAtomic(filepath.Join(settings.ConfigDir, "landing-policy.candidate"), policy, 0o644); err != nil {
		return err
	}
	if _, err := runHeadscaleCommand(ctx, docker, current.Container.ID, "headscale", "policy", "check", "--file", "/etc/headscale/landing-policy.candidate"); err != nil {
		return errors.New("deployer: Headscale rejected landing access rules")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	// Intent precedes the atomic policy replacement; retry can finish a crash
	// between either file write and SIGHUP. Infrastructure reconciliation also
	// uses this journal so an unrelated update cannot discard landing grants.
	if err := writeAtomic(filepath.Join(settings.ConfigDir, landingPolicyJournal), encoded, 0o600); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(settings.ConfigDir, "policy.hujson"), policy, 0o644); err != nil {
		return err
	}
	_, err = docker.ContainerKill(ctx, current.Container.ID, client.ContainerKillOptions{Signal: "SIGHUP"})
	return err
}

func (server *Server) applyLandingPolicy(writer http.ResponseWriter, request *http.Request) {
	manager, ok := server.installer.(deployapi.LandingPolicyManager)
	if !ok {
		writeError(writer, http.StatusConflict, errors.New("deployer: landing policy management is unavailable"))
		return
	}
	var input deployapi.LandingPolicyRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	server.headscaleMu.Lock()
	defer server.headscaleMu.Unlock()
	if err := manager.ApplyLandingPolicy(request.Context(), input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "applied"})
}
