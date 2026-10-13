# MVP CI policy

CI uses the version in the checked commit, not a repository-level alpha/full
switch. For `*-alpha` and `*-alpha.*` versions, the complete ordinary Go suite
runs without the race detector. This applies equally to pull requests, generated
version pull requests, manual dispatch and scheduled checks. Beta/stable versions
use the same test runner with race detection enabled.

## Pull requests

- Backend changes run formatting, API contract checks, vet/staticcheck, the full
  ordinary Go suite in four shards and cross-compilation.
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
