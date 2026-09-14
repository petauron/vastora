# IP quality diagnostics

The 3x-ui node list, landing server manager and exit chooser expose a manual
IP quality check. Each result describes that host's own observed public egress;
it is **not** a VLESS/HY2 entry-to-landing route test. Center uses the Agent's
reported public egress and local bind address. A report with a different IP is
rejected. A later address change marks the retained report stale.

Requires a Linux Agent advertising `ipQuality` and Docker. An older Agent must
be upgraded; Center never runs the check on its behalf. The `node.ip-quality`
task uses the existing authenticated, encrypted execution channel and lease.
Only one check per node can be pending/running. Ordinary detection failures
complete the diagnostic with an error code; they do not become configuration
failures. Lost execution authority or unconfirmed cleanup still requires the
normal Activity workflow. Nothing automatically retries or starts a check.

## Runner and privacy

- Upstream: [xykt/IPQuality](https://github.com/xykt/IPQuality), AGPL-3.0.
- Script: commit `ad222ab16778be2a13a174cd1acbd69fb4cac6b7`, checksum pinned in
  `internal/agent/ip_quality.go`. References use the same revision.
- Dependency image: `xykt/ipquality@sha256:26258a197cc03c186d41c26bd871d36ccc6ee269baf97762cfdd2b7d694c8dda`.
  The mutable upstream entrypoint is not used.
- Temporary container: unprivileged UID, no host mounts, read-only root,
  dropped capabilities, host network, 128 MiB memory including swap limit,
  half a CPU, 96 processes, 32 MiB temporary filesystem, 210-second lifetime.
  Docker auto-removes it, and Agent explicitly removes its exact container on
  all return paths. The entire operation, including image pull, is limited to
  four minutes. The fixed image remains cached for later manual checks.
- Privacy/JSON mode; no online report upload, statistics counter, advertisement,
  SMTP scan or bulk DNS blacklist scan. No speed/return-route test. Provider
  and streaming requests necessarily expose the tested exit IP to those sites.
- No raw report, coordinates, command, upstream response or report URL is
  retained. The authenticated Center stores the latest normalized report/IP,
  provider-specific risk values, unlock status/region/type and completion time.
  Missing values are not zero; sources are not averaged into a quality score.
- Schema 77 is additive and forward-only using Center's existing migration
  backup/fail-closed flow. Agent deletion cascades its latest diagnostic record.

No scan runs on page load or on a schedule. Opening the page reads saved
results; polling only observes pending/running checks.

## Targeted validation before release

Run the parser, runner-options and Center IPQuality tests, migration-equivalence
test, and the frontend IPQuality model/component checks when local verification
is authorized. On a non-production Linux node, manually run one check, verify
the IP matches, close/reopen the panel, then check cancellation and container
cleanup. Verify old Agents/offline nodes cannot queue a check and that changing
egress marks the previous report stale. Do not use production for validation.
