# Catalog runtime v4 acceptance checkpoint

This is an implementation checkpoint, not production approval. All Vastora
changes remain on the feature branch. Do not enable the independent publisher
or run the maintenance cutover based on unit tests alone.

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

Docker and systemd lifecycle tests currently use simulated engines/commands.
Their architecture fixtures do not establish execution on real amd64/arm64
hosts. No production application, catalog object, or signing secret was changed
during this checkpoint.

## Required before release/cutover

- Run isolated Linux Docker and systemd lifecycle acceptance on amd64 and arm64
  for applications not hard-coded into Vastora. Cover configuration, networking,
  health, logs, backup/restore, upgrade failure and retained-data uninstall.
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
