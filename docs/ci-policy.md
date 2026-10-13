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
