package center

import (
	"context"
	"database/sql"
	"strings"

	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/nodesecurity"
	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/nodeswap"
	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/tailscalehost"
	"golang.org/x/mod/semver"
)

// minCompatibleAgentVersion is the baseline Agent release that speaks the
// current schema-26 / Glivela control-plane contract. Center-only or UI-only
// releases do not raise this floor; raise an individual task kind's minimum
// below only when that task's Agent execution contract changes.
const minCompatibleAgentVersion = "0.2.0"

// minAgentVersionByTaskKind maps each non-exact-match Center task kind to the
// minimum Agent version required to execute it. Unknown task kinds fail closed
// to Center's running Version so newly added tasks cannot silently run on older
// Agents before their compatibility requirement is declared here.
var minAgentVersionByTaskKind = map[string]string{
	"agent.update":                    "",
	"application.adopt":               minCompatibleAgentVersion,
	"application.maintenance":         minCompatibleAgentVersion,
	"application.apply":               minCompatibleAgentVersion,
	"application.command":             minCompatibleAgentVersion,
	"landing.server.apply":            minCompatibleAgentVersion,
	"landing.proxy.apply":             minCompatibleAgentVersion,
	"node.listener.apply":             minCompatibleAgentVersion,
	"gateway.component.apply":         minCompatibleAgentVersion,
	"gateway.routes.apply":            minCompatibleAgentVersion,
	"tunnel.state.apply":              minCompatibleAgentVersion,
	"agent.decommission":              minCompatibleAgentVersion,
	tailscalehost.UpdateKind:          minCompatibleAgentVersion,
	nodesite.Kind:                     minCompatibleAgentVersion,
	nodesecurity.Kind:                 minCompatibleAgentVersion,
	nodeswap.Kind:                     minCompatibleAgentVersion,
	nodediagnostics.NetworkKind:       minCompatibleAgentVersion,
	nodediagnostics.ReturnRouteKind:   minCompatibleAgentVersion,
	nodediagnostics.BandwidthKind:     minCompatibleAgentVersion,
	nodediagnostics.HostProfileKind:   minCompatibleAgentVersion,
	nodediagnostics.LinkBandwidthKind: minCompatibleAgentVersion,
	nodediagnostics.LinkServerKind:    minCompatibleAgentVersion,
	ipquality.Kind:                    minCompatibleAgentVersion,
}

func normalizeSemver(version string) (string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if trimmed == "" {
		return "", false
	}
	canonical := "v" + trimmed
	if !semver.IsValid(canonical) {
		return "", false
	}
	return canonical, true
}

func minAgentVersionForTaskKind(kind string) string {
	if kind == osreinstall.TaskKind {
		return Version
	}
	if minVersion, ok := minAgentVersionByTaskKind[kind]; ok {
		return minVersion
	}
	return Version
}

// agentVersionCompatible reports whether an Agent reporting agentVersion meets
// minVersion. Empty agentVersion or empty minVersion fails closed. Non-empty
// synthetic non-semver versions (such as "test" in unit tests) remain
// unconstrained, while exact matching prerelease dev/lab builds (such as
// "0.1.0-dev" or "0.1.0-940lab.2") remain compatible with their own Center.
func agentVersionCompatible(agentVersion, minVersion string) bool {
	trimmedMin := strings.TrimSpace(minVersion)
	if trimmedMin == "" {
		return false
	}
	trimmedAgent := strings.TrimSpace(agentVersion)
	if trimmedAgent == "" {
		return false
	}
	current, currentOK := normalizeSemver(trimmedAgent)
	if !currentOK {
		return true
	}
	required, reqOK := normalizeSemver(trimmedMin)
	if !reqOK {
		return strings.TrimPrefix(trimmedAgent, "v") == strings.TrimPrefix(trimmedMin, "v")
	}
	if semver.Compare(current, required) >= 0 {
		return true
	}
	if centerVersion, centerOK := normalizeSemver(Version); centerOK &&
		semver.Prerelease(centerVersion) != "" &&
		semver.Compare(centerVersion, required) < 0 &&
		semver.Compare(current, centerVersion) == 0 {
		return true
	}
	return false
}

// agentVersionCompatibleForTaskKind reports whether an Agent reporting
// agentVersion satisfies the version compatibility contract for kind.
//
//   - agent.update has no minimum version requirement so older Agents can always
//     claim self-updates.
//   - os.reinstall requires an exact match with Center's running Version because
//     backup verification checks the exact Agent release.
//   - Declared task kinds require agentVersion >= their declared minimum.
//   - Undeclared task kinds fail closed to Center's running Version (and fail
//     closed when Center's Version or agentVersion is empty).
func agentVersionCompatibleForTaskKind(agentVersion, kind string) bool {
	if kind == "agent.update" {
		return true
	}
	if kind == osreinstall.TaskKind {
		centerTrimmed := strings.TrimPrefix(strings.TrimSpace(Version), "v")
		return centerTrimmed != "" && strings.TrimPrefix(strings.TrimSpace(agentVersion), "v") == centerTrimmed
	}
	return agentVersionCompatible(agentVersion, minAgentVersionForTaskKind(kind))
}

func agentVersionCompatibleForTask(agentVersion string, task *AgentTask) bool {
	if task == nil {
		return true
	}
	return agentVersionCompatibleForTaskKind(agentVersion, task.Kind)
}

// reviewedPackageRetryAllowed permits an exact operator-reviewed package retry
// (same task, same contract, ReceiptSHA256 == "") when an Agent meets the
// baseline control-plane contract even if application.apply later requires a
// higher minimum version. Without this exception, a node reporting
// runtime_recovery could neither finish its reviewed retry nor accept an
// agent.update (which requires recovery to clear first).
func (s *Store) reviewedPackageRetryAllowed(ctx context.Context, tx *sql.Tx, agentID, agentVersion string, task AgentTask) (bool, error) {
	if task.Kind != "application.apply" || task.Attempt < 2 || !agentVersionCompatible(agentVersion, minCompatibleAgentVersion) {
		return false, nil
	}
	if err := s.attachPackageRecovery(ctx, tx, agentID, &task); err != nil {
		return false, err
	}
	return task.PackageRecovery != nil && task.PackageRecovery.ReceiptSHA256 == "", nil
}
