# Vastora Agent Rules

## Petauron team: alpha-stage MVP delivery

- Prioritize a usable, end-to-end implementation of the approved requirement. Finish existing work and fix confirmed problems before proposing additional features, abstractions or refactors. One owner handles each feature end to end; do not split frontend/backend ownership or duplicate another chat's work.
- Do not add tests, coverage targets, test matrices, testing frameworks or additional test environments during alpha unless the user explicitly requests them. Comprehensive test coverage is not an alpha delivery requirement. Focus on basic usability and the smallest verification necessary for the approved delivery. Do not proactively run local tests, builds, linters, type checks or browser QA; run them only when the user explicitly requests local verification. Repository-required CI gates may still run as part of an explicitly requested push, merge or release workflow.
- Preserve existing required CI gates. For an existing failure, distinguish a product defect from a test-fixture defect and make the smallest relevant correction. Do not bypass gates, fabricate results or remove checks merely to report success. Passing a configuration check or a partial test is not proof of a usable deployment.
- Before introducing any new project dependency, obtain explicit user approval. Explain its purpose and necessity, whether existing dependencies can satisfy the need, and its maintenance, security and size costs. This includes runtime, development and testing dependencies; do not add first and ask afterward.
- Keep implementation, verification, CI, commit/push, merge, release and production deployment distinct. Respect the authorization for each stage, protect existing data and other chats' uncommitted work, and never infer production permission from a development or documentation request.
- Report meaningful progress, failures and decisions needed, rather than repeated unchanged status. When idle, inspect existing issues and propose a small complete next step; do not start unapproved work merely to stay busy.

- Do not preserve obsolete API or implementation compatibility. Remove superseded implementations outright; do not add aliases, dual code paths, or runtime fallbacks.
- Released Center database schemas must use tested, forward-only migrations. Back up before migrating, fail closed on migration errors, and do not support automatic database downgrade.
- Choose the simplest implementation that fully satisfies the current requirements. Avoid speculative abstractions, unnecessary indirection, and redundant configuration layers.
- Build the system incrementally in vertical slices. Make the smallest end-to-end version work before adding more layers, and never dismantle working functionality to accommodate unfinished complexity.
- Keep components modular and maintain a clear separation of concerns.
- Prefer mature, actively maintained libraries. Do not reimplement established functionality without a clear, documented reason.
- Inspect the capabilities of existing project dependencies before adding a new package or writing custom code. Do not assume the required capability is missing.
- Make architectural decisions for the long term. Do not introduce temporary designs with the intention of replacing them later.
- Study how mature products solve the same problem and follow proven patterns instead of inventing a solution from scratch.
- Do not proactively run local tests, builds, linters, type checks, or browser QA. Only run them when the user explicitly requests local verification. Repository-required CI gates may still run as part of an explicitly requested push, merge, or release workflow.

## GitHub releases and Actions storage

- Use Release Please v5, pinned to a reviewed full commit SHA, with the built-in
  `GITHUB_TOKEN`. Do not add a PAT or a separate release-token secret. Grant only
  the job permissions needed; never weaken branch protection to enable releases.
- Release through the generated version PR and the repository's Release Please
  config/manifest. Use Conventional Commits, including the final squash title;
  do not hide a releasable fix under a `ci:` or `chore:` title. Do not manually
  bump versions, move tags, or add a second tag-triggered release path.
- Required checks must represent real checks on the exact version PR head SHA.
  When token-created PRs do not trigger them, explicitly dispatch the existing
  workflows. Metadata validation is a separate check, not a substitute for CI
  or CodeQL; never manufacture successful required-check results.
- Publish only from an immutable, checked commit on protected `main`. Use the
  Release Please output SHA/tag throughout checkout, build, provenance and
  publication; fail closed on identity/version mismatches or failed checks.
  Do not assume a tag created with `GITHUB_TOKEN` triggers another workflow.
- Keep releases as drafts until all required builds, integrity/provenance checks
  and uploads succeed. Retain only the distribution files and metadata required
  by users or verified consumers. Retry failed jobs or an explicitly documented
  recovery flow; never overwrite an already published release or move its tag.
- CI and manually dispatched maintenance workflows do not retain downloadable
  Actions artifacts: no binaries, UI bundles, browser evidence, source patches,
  workspace copies or build records. Do not add upload/download-artifact steps
  or retention-based exceptions for these files. Only explicitly requested test
  coverage reports may be uploaded, with narrowly scoped contents and short,
  documented retention. Keep the actual tests and required CI gates.
- Set `DOCKER_BUILD_RECORD_UPLOAD=false` for Docker build actions. Keep bounded
  dependency/build caches for CI speed; caches are not release artifacts.
  Stage necessary cross-job release files directly in the draft Release rather
  than keeping duplicate Actions artifacts.
- Artifact cleanup must enumerate exact targets, skip active runs and artifacts
  required to recover failed releases, and preserve published Release assets and
  caches unless separately authorized. Never include credentials, production
  data, private host details or full subscription URLs in files or logs.
- Distinguish workflow edits, successful CI, successful publication and production
  deployment in completion reports. A green preparation job with skipped publish
  jobs is not proof of a release. Documentation edits do not authorize a push,
  merge, release, signing operation or production deployment.

- Keep Center/Agent program releases separate from signed application catalog
  publication and managed application upgrades. Preserve the catalog's protected
  signing and monotonic revision rules; an app-only update must not require a
  Vastora program release or automatically upgrade an installed instance.
