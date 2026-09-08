# Center API v1

The canonical machine-readable contract is [`openapi.json`](openapi.json). It is OpenAPI 3.1 and covers every method and path registered below `/api/v1` by Center.

Authentication is divided into four explicit audiences:

- **Browser administrator reads** require the `vastora_session` SameSite cookie.
- **Browser administrator mutations** require that cookie and the matching `X-CSRF-Token` header.
- **Agent control-plane calls** require the bearer credential bound to the Agent id in the path.
- **Bootstrap calls** are unauthenticated unless the operation declares a one-time enrollment bearer token. `GET /api/v1/setup/status` optionally accepts an administrator session and returns only non-sensitive bootstrap data otherwise.

JSON request bodies must use `application/json`, contain exactly one value, remain at or below 1 MiB, and contain no unknown fields. General errors use the JSON shape `{ "code": "invalid_request", "error": "..." }` with allowlisted, user-facing text, never raw internal errors. Login failures additionally return `captchaRequired`; they do not expose retry durations, failure thresholds, lockout durations or a `Retry-After` header. The login security challenge still requires its public site key. Unauthenticated setup responses leave network addresses and deployment capability fields empty or false; authenticated setup retains them. The contract declares binary downloads and server-sent event streams separately from JSON responses.

Regenerate the checked-in document after changing Center routes:

```sh
node scripts/generate-openapi.mjs
```

## Runtime recovery and REALITY selection

Agent heartbeat and administrator node views carry a bounded `runtimeRecovery` code:
empty when recovery is complete, or `pending`, `reconciliation`, `application`,
`gateway`, `listener`. A connected management channel is not proof of application
readiness; while recovery is pending, gateway/listener health is false. Raw host
errors remain in local diagnostics. Fresh observations restore only the previously
confirmed network identity/address; they never silently choose a different IP.

REALITY verification accepts either `recommend: true` or explicit `targetHost` and
`serverName`. The operation runs on the application node and returns checked
candidates, their fixed IPs and bounded measurements. Creation requires
`verificationId` and `targetIp` in addition to the selected hostname/SNI. The proof
must be successful, at most 15 minutes old, and match the current application,
node key and network profile. No implicit default or controller-side DNS recheck
substitutes another IP. See [target policy](reality-targets.md).

## Recovery readiness

`GET /api/v1/recovery` requires the administrator session and returns `no-store`.
It lists authoritative, reconstructible and application-owned components with
`ready | action_required | unsupported`, bounded reasons, identity fingerprints,
artifact digests and verification times. It accepts no remote URL, local path or
password. Backup inspection/registration is a host-local CLI operation.

Center-only backup does not establish cluster readiness. External volume evidence
is explicitly operator-attested; this view does not prove off-host storage or a
successful complete cluster drill. See [disaster recovery](disaster-recovery.md).

`go test ./internal/center` validates the document with `kin-openapi`, compares its method/path/security set with the Go route registry, and checks representative bootstrap, administrator, Agent, Catalog, publication, update, and integration behavior. `node scripts/generate-openapi.mjs --check` additionally verifies that the generated document is current.
