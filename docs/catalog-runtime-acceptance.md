# Catalog runtime v4 acceptance checkpoint

This is an implementation checkpoint, not production approval. Vastora changes
are reviewed in PR #716. Catalog publication is triggered manually after a
successful application release; the Pulse notification PR #25 was closed. The shared
module is published as petauron/catalog v0.2.0 and pinned without a development
workspace replacement. The earlier local-verification bullets below predate this
dependency update and the schema 102 merge; current CI and the bounded historical
copy rehearsal are recorded separately. Do not enable the independent publisher
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

The real-host CI matrix passed on Linux amd64 and arm64 in run 36254810439:
actual Docker/systemd installation, metadata-only adoption of copied v4 receipts,
upgrade, cold backup, data restore, logs and retained-data uninstall. These tests
use disposable hosted runners and neither execute production applications nor
prove adoption of a historical production installation. They exposed and led to
fixes for stale Docker resume IDs and systemd startup confirmation ordering.

On 2026-09-27, PR #716 at `aed5509` passed the
full manually dispatched CI run
[36318150489](https://github.com/petauron/vastora/actions/runs/36318150489):
Go quality/static analysis, race tests, web, security, cross-compile, both
container-image-security architectures and the final gate. The PR's separate
required checks also passed, including the Linux amd64/arm64 package host
lifecycle. This verifies the branch, not a released or deployed Center/Agent.

Production signing remains disabled. The catalog-signing environment is restricted
to protected branches with administrator bypass disabled; online signer keys and
the operator-approved shared R2 secrets are configured. The production gate and
historical maintenance acceptance remain outstanding. This catalog-runtime
rehearsal did not modify any production application or live catalog object.
Root v2 is public metadata signed by the
original offline root key; v1 is retained unchanged. The long-lived expiry
policy was reviewed and merged in independent catalog PR #4. An unchanged
catalog needs no renewal workflow; publication is a manual dispatch after a
successful application release.

On 2026-09-27, `TestCatalogV4HistoricalCopyRehearsal` passed against a private,
keyless copy of a historical production schema 100 backup on the earlier branch
revision targeting schema 101. It checked copy integrity, the pre-migration
backup, preserved historical row digests and catalog evidence, and pending-only
adoption records. After `main` independently introduced the Meridian UI schema
101 migration, the combined target is schema 102. The earlier rehearsal does
not validate this final two-step migration and must be repeated on a new copy.
The live Center database stayed on schema 100. This is database-migration
evidence only: it did not adopt actual Agent resources or restore application
data. A separate Center/Agent release occurred during the earlier rehearsal,
so no production no-restart assertion can be made from that observation window.

The same day, an isolated A1 copy of that historical schema 100 snapshot and
its matching root key passed the current `0.1.0-alpha.249` Center's encrypted
backup and restore commands. The restored key matched byte-for-byte, and both
source and restored databases passed SQLite `quick_check` with schema 100 and
37 application rows. The root-only temporary copy, password and encrypted
archive were removed after verification. This tests the Center backup format
and restore path against historical data; it does not establish off-host
storage, application-volume recovery, or live control-plane cutover.

The A1 Agent's current `0.1.0-alpha.249` recovery export and isolated restore
also passed without restarting the Agent. The encrypted artifact contained
`agent.db`, `agent.key`, `host-install.env` and the original `tailscaled.state`.
The restored schema 21 database passed `quick_check` and foreign-key checks with
five applied installations; the three identity/ownership files matched their
source bytes. The root-only temporary artifact and restored directory were
removed. This covers one Agent only, not the other nodes, off-host storage or
application-owned data.

Later on 2026-09-27, the current schema 102 migration test passed on an
unmodified, keyless, private snapshot of the live A1 schema 100 database. A
Linux arm64 test binary built from PR #716 with the pinned catalog module was
transferred to A1 and verified by SHA-256 before running only
`TestCatalogV4HistoricalCopyRehearsal` against the 493,264,896-byte snapshot.
The test created a pre-migration backup, preserved the checked historical row
digests, 37 applications and 16 manifest identities, and left the copied database
at schema 102 with `quick_check=ok`. The live database remained at schema 100;
Center/Pulse container IDs and start times and the Agent MainPID/InvocationID
were unchanged before and after. This exercises the two-step migration against
a full historical database copy, but does not prove actual Agent resource
adoption, application-volume recovery, or a live no-restart cutover.

On 2026-09-27, a read-only preflight resolved the already published Pulse
`v0.1.0-alpha.5` against its protected source commit and successful release run.
GitHub artifact attestations, `SHA256SUMS`, both native architectures and the
multi-platform OCI index passed the catalog source verifier. Applying those
verified coordinates to the reviewed recipes also passed `catalog-check
--artifacts`, including native ELF/archive and pinned OCI platform checks for
the complete candidate catalog. The check used an isolated local cache and did
not sign, upload, dispatch a publication or change a running application.
The old public timestamp is still revision 7 and expires 2026-10-02T18:12:57Z;
the production migration and single-writer cutover must be completed separately.

## Required before release/cutover

- Keep the real-host matrix green on the final release commit. Extend acceptance
  to permission/secret boundary checks and application-specific health/configuration
  behavior, beyond the generic process/data lifecycle already exercised.
- Rehearse adoption with copied historical state and real runtime resources;
  compare container ID/StartedAt, MainPID/InvocationID, mounts, ports, config
  digests and pairing before/after. For an unconfigured Meridian instance,
  verify no endpoint, Agent runtime row or reserved container before accepting
  an explicit dormant receipt, and verify no resource appeared afterward. Mock
  receipts do not prove no restart.
- Verify recovery on data copies, including refusal to run the old application
  against data already migrated by the new version.
- Finish cross-repository review and PR integration. Confirm the consumer is
  tested with its pinned published module, with no local workspace replacement.
- Keep the protected publication environment and operator-approved shared R2
  credentials scoped to the catalog repository. There is no cross-repository
  dispatch App or scheduled publisher. Do not attempt to export GitHub secrets.
- Freeze old writes, preserve/import exact historical release records, resolve
  uncertain tasks and independently back up Center, Agents and application data.
- After rehearsal and the agreed maintenance window, establish a single writer,
  retain trust/high-water history, and verify manual publication of a released
  Pulse version becomes visible without a Vastora source change or an automatic
  application upgrade.

The default Docker context on the development machine must not be used as an
isolated test target. Select an explicitly disposable test environment instead.
See [the cutover guide](catalog-release-guide.md) for the operational sequence.
