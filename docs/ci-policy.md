# MVP CI policy

CI uses the version in the checked commit, not a repository-level alpha/full
switch. For `*-alpha` and `*-alpha.*` versions, the complete ordinary Go suite
runs without the race detector. This applies equally to pull requests, generated
version pull requests, manual dispatch and scheduled checks. Beta/stable versions
use the same test runner with race detection enabled.

## Pull requests

- Backend changes run formatting, API contract checks, vet/staticcheck, the full
  ordinary Go suite (four Center shards plus all other packages in parallel)
  and cross-compilation.
- Frontend changes run lint, tests and build.
- Host/network integration runs for relevant runtime, installer, dependency and
  workflow changes. It is not run for every unrelated Center change.
- Release/upload tooling, installer-worker changes and plain changelog edits do
  not trigger host/network experiments. Version/manifest changes still trigger
  real full checks. Changes to the Go test entry point run the Go suite; changes
  to the shared classifier or composite Actions trigger full verification.
- The amd64 integration runner builds only the Dante artifact it executes, and
  reuses one compiled Agent integration test binary. Release image builds retain
  amd64 and arm64 coverage.
- CodeQL analyzes affected languages. Privacy fixtures and incremental commit
  secret detection remain enabled. PR secret scanning covers base..head, not the
  repository's entire history.
- Dependency vulnerability tools run when dependency manifests change. Duplicate
  ordinary regression invocations and the overlapping repository Trivy scan are
  removed. All regressions remain part of the full Go suite.
- PRs do not build two container images solely for vulnerability scanning.

## Release and scheduled checks

Generated version PRs run real CI and both CodeQL analyses on their exact head,
including when explicitly dispatched with GITHUB_TOKEN. Metadata validation does
not replace tests. Manual/scheduled CI runs all applicable suites, but never
enables race on an alpha version.

Release builds scan the final amd64 and arm64 image digests before tag promotion.
Image smoke tests, provenance, installer checksums and public download validation
remain mandatory. Weekly released-image scanning detects newly disclosed CVEs.
Alpha release housekeeping stays in the separate reconciliation workflow.

Branch protection is unchanged. No checks are replaced with synthetic success.
Program releases, signed catalog publication and production upgrades stay separate.

## Generated version PR approval and scheduling

The release job validates generated metadata first, records the checked-out PR
head SHA, and looks up CI and CodeQL runs for that exact head and managed branch.
The PR checkout is isolated in `.release-pr`; metadata validation and scheduling
execute only the scripts from the protected release baseline, not PR-supplied code.
It prefers the automatic PR runs, including `action_required` runs, because they
also supply GitHub's code-scanning PR context. Existing queued, running, successful
or failed runs do not cause another dispatch. A failure is not hidden by selecting
an older successful run or by automatically rerunning it.

GitHub requires a maintainer to select **Approve workflows to run** for
`GITHUB_TOKEN`-created PRs. The release job summary links the version PR and both
existing runs as soon as they are registered, instead of running a second full
suite while approval is still pending. Human approval is retained; no PAT, extra
secret, automatic approval or change to repository protections is introduced.
See [GitHub's token-triggered workflow policy](https://docs.github.com/en/actions/concepts/security/github_token).

Only if a required workflow has no exact-head PR or dispatch run after a bounded
registration grace period (up to four seconds per workflow) does the existing
serialized release job dispatch that workflow. API errors fail closed; the helper
checks the same-repository bot PR identity and expected head again before dispatch.
A moving head stops scheduling rather than borrowing another commit's checks.
This step reports scheduling/approval state only, not CI success or permission to
merge. All real CI/CodeQL and code-scanning gates remain mandatory. It never
approves, cancels or reruns existing workflows, and does not alter a running
release, publication, signing or production deployment.

## Integration build reuse

- Cache only the small native Dante build output (compressed binary, checksum
  and distribution notices), keyed by OS/architecture and all three build inputs.
  There are no fallback keys. Cache hits skip both BuildKit setup and compilation;
  checksum/gzip validation and all kernel, systemd and network tests still run.
  Cached data is a build cache, not a downloadable Actions artifact or a release.
- Related source/build changes on protected main run only the integration job
  plus classification/gate, populating its Go build cache and Dante output cache
  in the default-branch scope. Other PR suites do not repeat on these pushes.
- PR caches remain isolated. We do not export untrusted PR caches into main or
  give pull requests write access to a shared registry. GitHub evicts old build
  caches; no workflow stores production data, credentials or test logs in them.
- Compare cold and warm runs on the same commit, including whole-workflow time:
  a faster integration job does not necessarily shorten an unrelated bottleneck.

## Go test placement

- Center shards use the live `go test -list` inventory, including examples and
  fuzz seeds. A small checked-in timing table weights slower tests; deterministic
  longest-first placement balances four shards. Unknown/new tests receive a
  positive default weight and always run. Stale timing entries cannot add tests.
- All non-Center packages run independently instead of after shard 1, with a
  separate job-scoped build cache. Center shards share only their Center cache;
  the first shard saving it no longer prevents other-package output being saved.
- Verbose Go output records per-test timing in normal CI logs, not downloadable
  artifacts. Timing data changes placement only, never assertions or timeouts.
  Review/update slow-test weights from actual CI when the distribution drifts.
- The required gate waits for both Center and other-package jobs. Alpha still
  omits race, while beta/stable run the same complete inventory with race.
