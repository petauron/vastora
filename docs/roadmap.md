# v0.1 roadmap

Vastora is intentionally released as complete vertical slices, not as a
collection of empty screens or speculative interfaces. The current foundation
implements catalog signing and refresh, encrypted Master/Node local state,
administrator setup, encrypted Master backup and recovery, and the bilingual
catalog console.

The following slices remain before `v0.1.0`:

1. Contract tests for API, app manifest, config schema, and catalog envelope.
2. Catalog signing CLI interoperability fixtures.
3. Master migration, secret encryption, and backup recovery hardening.
4. Bootstrap setup, Argon2id authentication, and security sessions.
5. Multi-source catalog refresh, cache, and management API.
6. Node enrollment, HTTPS long polling, task leases, retries, and revocation.
7. Node encrypted cache and Master-offline restart.
8. Declarative Compose rendering, host-permission checks, and deployment state.
9. Private Registry credential use and temporary pull configuration.
10. English and Simplified Chinese installation wizard and store workflow.
11. CPA installation, expected-401 health check, and offline recovery test.
12. Master/Node TUI installer, migration recovery, and release hardening.

An app's only deployable identity is `source-id/app-id` plus its immutable
semantic `version`; an image digest is part of that version's content. There
are no marketplace revisions, old-version selector, automatic rollback, leader
election, or silent cross-source overrides in v0.1.
