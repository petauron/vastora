# Route health diagnostics

Diagnostics explain existing entry-to-egress checks; they do not authorize
traffic, extend the 15-second kernel lease, add probe destinations, change
certificate validation, or restart a proxy after a failed check.

The Agent retains only its current per-peer observation in memory and sends it
through the authenticated heartbeat. It reports check start/completion, elapsed
milliseconds, private transport state, a fixed failure stage/reason, HTTP status,
last successful check and lease deadline. The existing maintained SOCKS dialer
owns negotiation; diagnostic instrumentation distinguishes TCP connect from the
SOCKS handshake without implementing a new SOCKS client.

Center schema 112 adds the latest diagnostic snapshot and transition history.
Released databases use the existing backup-before-migration, fail-closed Goose
flow. No automatic downgrade is supported. History contains only semantic
changes: healthy/blocked, failure stage/reason, HTTP status or exit family.
Changing probe clocks or durations does not append an event. History is capped
at 200 events per route and 10,000 globally; events older than seven days are
removed by hourly retention. A bounded Center worker records lease expiry even
when an entry stops reporting, without changing route authority. It preserves
the last actual receipt time separately from the expiry observation time.

The admin-authenticated, non-cacheable read endpoint is
`GET /api/v1/meridian/routes/{id}/health`. It returns current evidence, server
read time and at most 20 recent transitions. IPv6/IPv4 composite routes expose
separate primary and IPv4 legs. The Glivela route details load this endpoint only
when expanded or explicitly refreshed; reading it never starts a probe or task.

Never persist arbitrary remote error strings, URLs, response bodies, protocol
UUIDs, proxy passwords, private keys, Tailscale keys or full subscription links.
Stored exit information is the address family, not the public IP. Unknown
diagnostic codes are represented as `unreported`, not copied into the history.

## Verification

Regression coverage includes transition-only history, recovery, privacy,
non-mutating fingerprints, last-success retention, expiry versus receipt clocks,
history limits, cascade deletion, forward migration and lazy/error UI states.
Local tests/builds/type checks/browser QA require explicit user authorization.
Publish the compatible Center/Agent before the updated Glivela UI; publishing
its signed catalog and upgrading production remain separate approval steps.
