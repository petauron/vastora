# Meridian route usage and host traffic budgets

`GET /api/v1/meridian/traffic` is an authenticated, non-cacheable read. It groups
retained credentials by entry node and native/fixed egress, across accounts.
Upload/download use the client's perspective. No token or credential identity is
returned. Loading or refreshing the view never probes a node or changes runtime
configuration.

Directional totals begin at each credential's first authenticated sample after
schema 108. That sample establishes a baseline; existing combined usage cannot be
split retrospectively. Subsequent direction deltas and existing quota watermarks
commit in the same receipt transaction. Duplicate counters add zero. Decreasing
counters start a new process counter interval. Account reset does not reset these
observational lifetime totals. They are not monthly totals or a billing ledger.

The earliest tracking start and latest sample are shown. Different credentials
can start at different times. Missing credential samples produce partial coverage;
no tracked credentials means missing, never zero. A retained line is historical
when active credentials have no sample within 15 minutes. Reporting gaps and
unobserved process resets cannot reconstruct lost traffic. Retained revoked
credentials still contribute; removal of the owning business records also removes
their observational totals through the existing deletion lifecycle.

Host traffic remains Pulse-owned. The Meridian view links to the existing ready,
authenticated HTTPS Pulse dashboard using the same access checks as the Pulse
workspace. In Pulse, configure budget, counting direction (sum or outbound for
the MVP), monthly UTC reset day (1–28), and threshold alerts. Host totals include
other applications and transport overhead; provider billing remains authoritative.
A missing Pulse entry is unavailable, never substituted with route counters.
These host budget alerts do not stop Meridian routes. Existing explicitly
configured account and entry quota enforcement is unchanged.

Migration 108 uses the normal forward-only, backup-before-upgrade Center path;
no existing usage, credentials, quota or runtime revision is rewritten. CI covers
migration preservation, baseline, grouping, repeated receipts, counter reset,
transaction rollback, stale/partial display, and read-only revision preservation.
No production acceptance is implied by source or CI completion.
