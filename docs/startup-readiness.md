# Startup-only route readiness

Activation, upgrade and a new Xray worker generation require REALITY warmup and
identity-bound end-to-end acceptance for configured landing peers. Glivela caches
acceptance for the exact process, configuration and revision. The legacy Agent
landing executor also checks once at activation. Closed startup fences are
removed only after acceptance; static peer/source firewall isolation stays.

Running workers do not periodically probe external HTTPS targets, renew timed
permissions, or interrupt traffic because a test target fails. Completed route
challenges do not expire or automatically retry by age. Explicit diagnostic
requests remain possible; pending challenge admission keeps its bounded replay
window. Heartbeats still validate local process/configuration state.

Subscriptions retain account expiry/quota, credential revocation, manual disable
or suspension, endpoint retirement, identity and applied-revision rules. Saved
snapshots cannot override these rules. Center schema 120 invalidates old timed
route authorizations once, preserving configuration, credentials and accounting;
compatible upgraded nodes must send new startup acceptance. The existing
forward-only migration path backs up first and fails closed on errors.

Manual firewall confirmation remains separate: it requires nonempty recent
startup evidence obtained after the firewall application. Diagnostic retention
never launches probes. Glivela and Center releases/deployment require separate
authorization; this task only produces local commits and local test results.

This document supersedes earlier descriptions of periodic health leases,
success/failure streaks and probe-triggered withdrawal in existing documents.
Those older descriptions are retained for historical context.

## Local validation — 2026-10-25

- `go test ./internal/meridianruntime ./internal/landing ./internal/agent ./internal/center`: passed.
- Regression coverage includes startup proof lifetime, quota/revocation/disable behavior, subscription snapshots, forward migration and preservation of revocation state.

Self-review completed for changed execution, receipt, publication and migration
paths. Existing CI workflows and dependencies are unchanged. No CT116 or
production execution, push, merge or deployment was performed.
