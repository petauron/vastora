package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

const controlPlaneSwitchJournalVersion = 1

type controlPlaneSwitchFile struct {
	Path    string      `json:"path"`
	Exists  bool        `json:"exists"`
	Content []byte      `json:"content,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

type controlPlaneSwitchJournal struct {
	Version                    int                      `json:"version"`
	TargetCenterURL            string                   `json:"targetCenterUrl"`
	Committed                  bool                     `json:"committed"`
	TailscaleInstalled         bool                     `json:"tailscaleInstalled"`
	TailscaleStateExisted      bool                     `json:"tailscaleStateExisted"`
	TailscaleActive            bool                     `json:"tailscaleActive"`
	TailscaleEnabled           bool                     `json:"tailscaleEnabled"`
	AgentActive                bool                     `json:"agentActive"`
	AgentEnabled               bool                     `json:"agentEnabled"`
	PreviousTailscaleProfileID string                   `json:"previousTailscaleProfileId,omitempty"`
	PreviousTailscaleProfiles  []string                 `json:"previousTailscaleProfiles"`
	PreviousRoles              []string                 `json:"previousRoles"`
	PreviousCapabilities       agent.Capabilities       `json:"previousCapabilities"`
	Files                      []controlPlaneSwitchFile `json:"files"`
}

type controlPlaneSwitchEnvironment struct {
	journalPath string
	unitPath    string
	stateDir    string
	paths       []string
	lookPath    func(string) (string, error)
	removeAll   func(string) error
	run         func(context.Context, string, ...string) ([]byte, error)
	verify      func(context.Context, *agent.Store, []string, agent.Capabilities) error
}

func defaultControlPlaneSwitchEnvironment(dataDir string) controlPlaneSwitchEnvironment {
	privacyOverride := defaultTailscaleIsolationEnvironment().overridePath
	endpoint := defaultTailscaleEndpointEnvironment()
	return controlPlaneSwitchEnvironment{
		journalPath: filepath.Join(dataDir, "control-plane-switch.json"),
		unitPath:    vastoraAgentUnitPath,
		stateDir:    "/var/lib/tailscale",
		paths: []string{
			filepath.Join(dataDir, "host-install.env"),
			vastoraAgentUnitPath,
			privacyOverride,
			tailscalePrivacyAppliedPath(privacyOverride),
			defaultTailscaleIsolationEnvironment().hostsPath,
			defaultTailscaleIsolationEnvironment().derpCache,
			endpoint.configPath,
			endpoint.overridePath,
			"/usr/share/keyrings/tailscale-archive-keyring.gpg",
			"/etc/apt/sources.list.d/tailscale.list",
		},
		lookPath:  exec.LookPath,
		removeAll: os.RemoveAll,
		run:       runHostCommand,
		verify: func(ctx context.Context, store *agent.Store, roles []string, capabilities agent.Capabilities) error {
			verifyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return (agent.Client{Roles: roles, Capabilities: capabilities}).Heartbeat(verifyContext, store)
		},
	}
}

func beginControlPlaneSwitch(ctx context.Context, dataDir, targetCenterURL string, environment controlPlaneSwitchEnvironment) (bool, error) {
	targetCenterURL = strings.TrimRight(strings.TrimSpace(targetCenterURL), "/")
	if targetCenterURL == "" {
		return false, errors.New("begin Center switch: target Center URL is required")
	}
	if existing, err := readControlPlaneSwitchJournal(environment); err == nil {
		if existing.TargetCenterURL != targetCenterURL {
			return false, fmt.Errorf("begin Center switch: unfinished switch targets %s; roll it back first", existing.TargetCenterURL)
		}
		if existing.Committed {
			if err := finalizeCommittedControlPlaneSwitch(ctx, dataDir, existing, environment); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if operation, exists, err := agent.InspectInstallOperation(dataDir); err != nil {
		return false, fmt.Errorf("begin Center switch: inspect Agent installation: %w", err)
	} else if exists {
		return false, fmt.Errorf("begin Center switch: Agent installation phase %s has no host rollback journal", operation.Phase)
	}
	journal := controlPlaneSwitchJournal{Version: controlPlaneSwitchJournalVersion, TargetCenterURL: targetCenterURL}
	journal.TailscaleInstalled = environment.lookPath != nil && func() bool { _, err := environment.lookPath("tailscale"); return err == nil }()
	if info, err := os.Stat(environment.stateDir); err == nil {
		if !info.IsDir() {
			return false, errors.New("begin Center switch: Tailscale state path is not a directory")
		}
		journal.TailscaleStateExisted = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("begin Center switch: inspect Tailscale state: %w", err)
	}
	if !journal.TailscaleInstalled && journal.TailscaleStateExisted {
		return false, errors.New("begin Center switch: Tailscale state exists without the Tailscale command; refusing to overwrite an orphaned identity")
	}
	journal.TailscaleActive = serviceState(ctx, environment.run, "is-active", "tailscaled.service")
	journal.TailscaleEnabled = serviceState(ctx, environment.run, "is-enabled", "tailscaled.service")
	journal.AgentActive = serviceState(ctx, environment.run, "is-active", "vastora-agent.service")
	journal.AgentEnabled = serviceState(ctx, environment.run, "is-enabled", "vastora-agent.service")
	if journal.TailscaleInstalled {
		output, err := environment.run(ctx, "tailscale", "switch", "--list", "--json")
		if err != nil {
			return false, fmt.Errorf("begin Center switch: inspect Tailscale profiles: %s: %w", strings.TrimSpace(string(output)), err)
		}
		var profiles []struct {
			ID       string `json:"id"`
			Selected bool   `json:"selected"`
		}
		if err := json.Unmarshal(output, &profiles); err != nil {
			return false, fmt.Errorf("begin Center switch: decode Tailscale profiles: %w", err)
		}
		for _, profile := range profiles {
			if profile.ID == "" {
				return false, errors.New("begin Center switch: Tailscale returned a profile without an ID")
			}
			journal.PreviousTailscaleProfiles = append(journal.PreviousTailscaleProfiles, profile.ID)
			if profile.Selected {
				journal.PreviousTailscaleProfileID = profile.ID
				break
			}
		}
	}
	for _, path := range environment.paths {
		snapshot, err := inspectHostFile(path)
		if err != nil {
			return false, fmt.Errorf("begin Center switch: snapshot %s: %w", path, err)
		}
		journal.Files = append(journal.Files, controlPlaneSwitchFile{Path: path, Exists: snapshot.exists, Content: snapshot.content, Mode: snapshot.mode})
		if path == environment.unitPath {
			if !snapshot.exists {
				return false, errors.New("begin Center switch: existing Vastora Agent service is missing")
			}
			journal.PreviousRoles, journal.PreviousCapabilities, err = parseAgentRuntimeFromUnit(snapshot.content)
			if err != nil {
				return false, fmt.Errorf("begin Center switch: inspect existing Agent service: %w", err)
			}
		}
	}
	if len(journal.PreviousRoles) == 0 {
		return false, errors.New("begin Center switch: Agent service was not included in the rollback snapshot")
	}
	if err := writeControlPlaneSwitchJournal(environment, journal); err != nil {
		return false, fmt.Errorf("begin Center switch: %w", err)
	}
	return false, nil
}

func rollbackControlPlaneSwitch(ctx context.Context, dataDir string, environment controlPlaneSwitchEnvironment) error {
	journal, err := readControlPlaneSwitchJournal(environment)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var rollbackErrors []error
	if store, openErr := agent.Open(dataDir); openErr == nil {
		if rollbackErr := store.RollbackInstallOperation(ctx); rollbackErr != nil {
			rollbackErrors = append(rollbackErrors, rollbackErr)
		}
		_ = store.Close()
	} else if !errors.Is(openErr, os.ErrNotExist) {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous Agent enrollment: %w", openErr))
	}
	for _, file := range journal.Files {
		if restoreErr := restoreHostFile(file.Path, hostFileSnapshot{exists: file.Exists, content: file.Content, mode: file.Mode}); restoreErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", file.Path, restoreErr))
		}
	}
	if output, reloadErr := environment.run(ctx, "systemctl", "daemon-reload"); reloadErr != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("reload restored services: %s: %w", strings.TrimSpace(string(output)), reloadErr))
	}
	if !journal.TailscaleInstalled {
		if environment.lookPath != nil {
			if _, lookupErr := environment.lookPath("tailscale"); lookupErr == nil {
				_, _ = environment.run(ctx, "systemctl", "disable", "--now", "tailscaled.service")
				if output, removeErr := environment.run(ctx, "apt-get", "remove", "-y", "tailscale", "tailscale-archive-keyring"); removeErr != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("remove Tailscale installed by failed switch: %s: %w", strings.TrimSpace(string(output)), removeErr))
				}
			}
		}
		if !journal.TailscaleStateExisted && environment.removeAll != nil {
			if removeErr := environment.removeAll(environment.stateDir); removeErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove Tailscale state created by failed switch: %w", removeErr))
			}
		}
	} else {
		output, listErr := environment.run(ctx, "tailscale", "switch", "--list", "--json")
		if listErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("inspect Tailscale profiles during rollback: %s: %w", strings.TrimSpace(string(output)), listErr))
		} else {
			var currentProfiles []struct {
				ID string `json:"id"`
			}
			if decodeErr := json.Unmarshal(output, &currentProfiles); decodeErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("decode Tailscale profiles during rollback: %w", decodeErr))
			} else {
				previous := make(map[string]struct{}, len(journal.PreviousTailscaleProfiles))
				for _, id := range journal.PreviousTailscaleProfiles {
					previous[id] = struct{}{}
				}
				for _, profile := range currentProfiles {
					if _, existed := previous[profile.ID]; existed || profile.ID == "" {
						continue
					}
					if switchOutput, switchErr := environment.run(ctx, "tailscale", "switch", profile.ID); switchErr != nil {
						rollbackErrors = append(rollbackErrors, fmt.Errorf("select new Tailscale profile for removal: %s: %w", strings.TrimSpace(string(switchOutput)), switchErr))
						continue
					}
					if logoutOutput, logoutErr := environment.run(ctx, "tailscale", "logout"); logoutErr != nil {
						rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new Tailscale profile: %s: %w", strings.TrimSpace(string(logoutOutput)), logoutErr))
					}
				}
			}
		}
		if journal.PreviousTailscaleProfileID != "" {
			if output, switchErr := environment.run(ctx, "tailscale", "switch", journal.PreviousTailscaleProfileID); switchErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous Tailscale profile: %s: %w", strings.TrimSpace(string(output)), switchErr))
			}
		}
		restoreServiceState(ctx, environment.run, "tailscaled.service", journal.TailscaleActive, journal.TailscaleEnabled, &rollbackErrors)
	}
	restoreServiceState(ctx, environment.run, "vastora-agent.service", journal.AgentActive, journal.AgentEnabled, &rollbackErrors)
	if len(rollbackErrors) == 0 {
		store, openErr := agent.Open(dataDir)
		if openErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("verify restored Agent: %w", openErr))
		} else {
			if environment.verify == nil {
				rollbackErrors = append(rollbackErrors, errors.New("verify restored Agent: verifier is required"))
			} else if verifyErr := environment.verify(ctx, store, journal.PreviousRoles, journal.PreviousCapabilities); verifyErr != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("verify restored Agent heartbeat: %w", verifyErr))
			}
			_ = store.Close()
		}
	}
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("rollback Center switch: %w", errors.Join(rollbackErrors...))
	}
	if err := os.Remove(environment.journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear Center switch journal: %w", err)
	}
	return nil
}

func commitControlPlaneSwitch(dataDir, ownership string, enrolled bool, environment controlPlaneSwitchEnvironment) error {
	journal, err := readControlPlaneSwitchJournal(environment)
	if err != nil {
		return fmt.Errorf("commit Center switch: %w", err)
	}
	if ownership == "auto" {
		ownership = inferredTailscaleOwnership(journal, dataDir, enrolled)
	}
	if ownership != "managed" && ownership != "external" && ownership != "none" {
		return errors.New("commit Center switch: invalid Tailscale ownership")
	}
	state := fmt.Sprintf("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=%s\nTAILSCALE_ENROLLED=%d\n", ownership, boolInt(enrolled))
	if err := writeAtomicHostFile(filepath.Join(dataDir, "host-install.env"), state, 0o600); err != nil {
		return fmt.Errorf("commit Center switch: save host install state: %w", err)
	}
	journal.Committed = true
	if err := writeControlPlaneSwitchJournal(environment, journal); err != nil {
		return fmt.Errorf("commit Center switch: persist commit decision: %w", err)
	}
	store, err := agent.Open(dataDir)
	if err != nil {
		return fmt.Errorf("commit Center switch: open Agent state: %w", err)
	}
	completeErr := store.CompleteInstallOperation(context.Background())
	_ = store.Close()
	if completeErr != nil {
		return fmt.Errorf("commit Center switch: finalize Agent installation: %w", completeErr)
	}
	if err := os.Remove(environment.journalPath); err != nil {
		return fmt.Errorf("commit Center switch: clear rollback journal: %w", err)
	}
	return nil
}

func finalizeCommittedControlPlaneSwitch(ctx context.Context, dataDir string, journal controlPlaneSwitchJournal, environment controlPlaneSwitchEnvironment) error {
	store, err := agent.Open(dataDir)
	if err != nil {
		return fmt.Errorf("resume committed Center switch: open Agent state: %w", err)
	}
	connection, connectionErr := store.Connection(ctx)
	if connectionErr != nil || strings.TrimRight(connection.CenterURL, "/") != journal.TargetCenterURL {
		_ = store.Close()
		return errors.New("resume committed Center switch: current Agent does not use the committed Center")
	}
	if operation, exists, operationErr := store.InstallOperation(ctx); operationErr != nil {
		_ = store.Close()
		return operationErr
	} else if exists {
		if operation.Phase != "healthy" {
			_ = store.Close()
			return fmt.Errorf("resume committed Center switch: Agent installation phase is %s", operation.Phase)
		}
		if err := store.CompleteInstallOperation(ctx); err != nil {
			_ = store.Close()
			return err
		}
	}
	_ = store.Close()
	if err := os.Remove(environment.journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resume committed Center switch: clear rollback journal: %w", err)
	}
	return nil
}

func inferredTailscaleOwnership(journal controlPlaneSwitchJournal, dataDir string, enrolled bool) string {
	if !enrolled {
		return "none"
	}
	if !journal.TailscaleInstalled {
		return "managed"
	}
	hostStatePath := filepath.Join(dataDir, "host-install.env")
	for _, file := range journal.Files {
		if file.Path == hostStatePath && file.Exists && strings.Contains(string(file.Content), "TAILSCALE_OWNERSHIP=managed") {
			return "managed"
		}
	}
	return "external"
}

func readControlPlaneSwitchJournal(environment controlPlaneSwitchEnvironment) (controlPlaneSwitchJournal, error) {
	content, err := os.ReadFile(environment.journalPath)
	if err != nil {
		return controlPlaneSwitchJournal{}, err
	}
	var journal controlPlaneSwitchJournal
	if err := json.Unmarshal(content, &journal); err != nil {
		return controlPlaneSwitchJournal{}, fmt.Errorf("read Center switch journal: %w", err)
	}
	if journal.Version != controlPlaneSwitchJournalVersion || journal.TargetCenterURL == "" {
		return controlPlaneSwitchJournal{}, errors.New("read Center switch journal: unsupported or incomplete journal")
	}
	if len(journal.PreviousRoles) == 0 {
		return controlPlaneSwitchJournal{}, errors.New("read Center switch journal: previous Agent runtime is missing")
	}
	if len(journal.Files) != len(environment.paths) {
		return controlPlaneSwitchJournal{}, errors.New("read Center switch journal: file snapshot set is incomplete")
	}
	for index, path := range environment.paths {
		if journal.Files[index].Path != path {
			return controlPlaneSwitchJournal{}, errors.New("read Center switch journal: file snapshot path is invalid")
		}
	}
	return journal, nil
}

func writeControlPlaneSwitchJournal(environment controlPlaneSwitchEnvironment, journal controlPlaneSwitchJournal) error {
	content, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(environment.journalPath), 0o700); err != nil {
		return err
	}
	return writeAtomicHostFile(environment.journalPath, string(content)+"\n", 0o600)
}

func serviceState(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), action, unit string) bool {
	if run == nil {
		return false
	}
	stateContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := run(stateContext, "systemctl", action, "--quiet", unit)
	return err == nil
}

func restoreServiceState(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), unit string, active, enabled bool, rollbackErrors *[]error) {
	enableAction := "disable"
	if enabled {
		enableAction = "enable"
	}
	if output, err := run(ctx, "systemctl", enableAction, unit); err != nil {
		*rollbackErrors = append(*rollbackErrors, fmt.Errorf("restore %s enablement: %s: %w", unit, strings.TrimSpace(string(output)), err))
	}
	activeAction := "stop"
	if active {
		activeAction = "restart"
	}
	if output, err := run(ctx, "systemctl", activeAction, unit); err != nil {
		*rollbackErrors = append(*rollbackErrors, fmt.Errorf("restore %s activity: %s: %w", unit, strings.TrimSpace(string(output)), err))
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var agentUnitRuntimePattern = regexp.MustCompile(`--roles\s+("(?:[^"\\]|\\.)*")\s+--capabilities\s+("(?:[^"\\]|\\.)*")`)

func parseAgentRuntimeFromUnit(content []byte) ([]string, agent.Capabilities, error) {
	match := agentUnitRuntimePattern.FindSubmatch(content)
	if len(match) != 3 {
		return nil, agent.Capabilities{}, errors.New("Vastora Agent runtime flags are missing")
	}
	rolesValue, err := strconv.Unquote(string(match[1]))
	if err != nil {
		return nil, agent.Capabilities{}, err
	}
	capabilitiesValue, err := strconv.Unquote(string(match[2]))
	if err != nil {
		return nil, agent.Capabilities{}, err
	}
	return validatedNodeRuntime(rolesValue, capabilitiesValue)
}
