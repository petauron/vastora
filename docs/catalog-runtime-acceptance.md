# Catalog runtime v4 acceptance checkpoint

This is an implementation checkpoint, not production approval. Vastora changes
are reviewed in PR #716; Pulse notification is in petauron/pulse#25. The shared
module is published as petauron/catalog v0.1.0 and pinned without a development
workspace replacement. Do not enable the independent publisher or run the
maintenance cutover based on unit tests alone.

## Verified locally

- All Go packages pass `GOWORK=off go test ./... -count=1 -timeout=180s`
  using the pinned external catalog module, not the development workspace.
- Generic package admission includes recipe revision, manifest digest, explicit
  privileged-capability grants, and node executor capability checks.
- Successful deployment receipts must match the task/package identity and the
  signed runtime kind, and ready receipts must include owned resources.
- Historical adoption reads both SQLite TEXT and BLOB configuration without
  rewriting the original manifest. Fixtures check metadata-only adoption.
- Forward migration refuses unfinished or uncertain application operations;
  historical migration tests inspect the blocked database before modeling
  explicit operator resolution.
- Agent removal retains application state, receipts, backup paths and recovery
  keys by default, including unknown state-directory entries. Tests also cover
  repeated removal and rejection of a symlink state directory.
- The obsolete 3x-ui controller relocation entry point refuses work before
  changing state; business migration remains separate from package adoption.
- Frontend unit tests: 348 passing; TypeScript project build passing. These are
  not rendered-browser or deployed-UI evidence.
- CI workflow policy and release sequencing checks pass locally.

The real-host CI matrix passed on Linux amd64 and arm64 in run 36254810439:
actual Docker/systemd installation, metadata-only adoption of copied v4 receipts,
upgrade, cold backup, data restore, logs and retained-data uninstall. These tests
use disposable hosted runners and neither execute production applications nor
prove adoption of a historical production installation. They exposed and led to
fixes for stale Docker resume IDs and systemd startup confirmation ordering.

Production signing remains disabled. The catalog-signing environment is restricted
to protected branches with administrator bypass disabled; signing and upload
credentials are still operator prerequisites. No production application or live
catalog object was modified.

## Required before release/cutover

- Keep the real-host matrix green on the final release commit. Extend acceptance
  to permission/secret boundary checks and application-specific health/configuration
  behavior, beyond the generic process/data lifecycle already exercised.
- Rehearse adoption with copied historical state and real runtime resources;
  compare container ID/StartedAt, MainPID/InvocationID, mounts, ports, config
  digests and pairing before/after. Mock receipts do not prove no restart.
- Verify recovery on data copies, including refusal to run the old application
  against data already migrated by the new version.
- Finish cross-repository review and PR integration. Confirm the consumer is
  tested with its pinned published module, with no local workspace replacement.
- Provision the catalog-only dispatch App and protected publication environment
  through the operator's secure channel. Do not attempt to export GitHub secrets.
- Freeze old writes, preserve/import exact historical release records, resolve
  uncertain tasks and independently back up Center, Agents and application data.
- After rehearsal and the agreed maintenance window, establish a single writer,
  retain trust/high-water history, and verify Pulse release-to-catalog visibility
  without a Vastora source change or an automatic application upgrade.

The default Docker context on the development machine must not be used as an
isolated test target. Select an explicitly disposable test environment instead.
See [the cutover guide](catalog-release-guide.md) for the operational sequence.
