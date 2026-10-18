# Subscription preview

The Glivela account card exposes an on-demand, administrator-only preview at
`GET /api/v1/meridian/accounts/{id}/subscription-preview`.

## Ownership and selection

Center currently owns account/quota checks, live endpoint and route selection,
and the encrypted applied subscription snapshot. `MeridianSubscription` and
`MeridianSubscriptionPreview` share `resolveMeridianSubscriptionInTx`.

The preview observes rejection reasons in those real selection branches. SQL
acceptance conditions are evaluated once as a decision column; public requests
still select only accepted records. Diagnostic requests can observe rejected
records but cannot publish them. A route excluded during the live attempt is not
reported as missing if it is actually retained by the selected applied snapshot.

The preview uses the real `glivela.RenderLinks` output for names, protocols,
order and count. It does not reconstruct links or implement a second publication
filter. Native visibility is observed by comparing complete rendered URI identity
except the display fragment, not by comparing names or host addresses alone.
Glivela's ordinary and Mihomo encoders share one final inventory resolver; the
removed format-specific Mihomo composition path is not retained as a fallback.

## Snapshot and failure semantics

- Reads do not save or replace snapshots, start tasks, probe links or repair routes.
- Live partial results remain partial and do not overwrite the complete snapshot.
- Applied-snapshot reads still perform current revocation, quota, source-identity,
  landing and health-evidence checks. A snapshot cannot restore revoked authority.
- Responses identify live/imported/applied-snapshot output, the applied account
  revision, read time and a fingerprint of the **safe item metadata only**.
- Disabled, expired, exhausted or empty accounts return a successful diagnostic
  response with zero items and a reason. Public downloads retain their unavailable
  response. Corrupt state and rendering errors fail closed with a generic 503.
- Separate requests can observe later state; refresh after changes. A preview is
  not a reservation and does not guarantee an unchanged subsequent download.

## Credential boundary

Only explicit allowlist DTOs are serialized: names, protocol kinds, resource IDs,
reason codes and safe revision/time metadata. No subscription URL/token, connection
URI, protocol identity/password, secret record reference or encrypted snapshot is
returned. The endpoint requires the existing Center administrator session and
sets `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.

The UI requests only expanded previews, cancels closed or superseded requests,
has a 15-second timeout, and offers explicit refresh. Entry configuration and route
health details reuse existing screens; nothing automatically rewrites a route.

## Development and verification status

Changes span the isolated `vastora-subscription-preview` and
`glivela-subscription-preview` worktrees. No schema migration or dependency is
added. The Center adapter deliberately uses the existing rendering API, so its
source does not depend on an unpublished Glivela API. The dependency is fixed to
the checked Glivela PR #56 merge commit
`011a0efc977748d97afd195e1663da8a6775fae4`, resolved by Go as
`v0.1.0-alpha.24.0.20261018115648-011a0efc9777`. No floating branch or local
replacement is used.

## Local acceptance

The explicitly authorized local acceptance used synthetic data only. Go tests
used a temporary workspace containing the two feature checkouts, so Center was
also exercised against the modified Glivela renderer without inventing a module
version or committing a local replacement.

Passed commands (run in the respective repository):

```sh
# Glivela
go test . -run 'Test(Render|Resolved|Composite.*Subscription|CompositeNames)' -count=1
npm run check --prefix ui
npm test --prefix ui -- src/glivela/SubscriptionPreview.test.tsx src/glivela/Routes.test.tsx src/glivela/RouteHealth.test.tsx src/host-contract.test.tsx

# Vastora
go test ./internal/center -run 'Test(SubscriptionPreview|Meridian.*Subscription|Meridian.*Snapshot|Meridian.*RouteHealth|MeridianAppliedSnapshot|OpenAPIContractIsValidAndMatchesRegisteredRoutes)' -count=1
```

The UI run passed four files and ten tests. Coverage includes ordinary/Mihomo
inventory and ordering, one composite identity, hidden native entries, retained
snapshots, partial live output, expired evidence, zero results without direct
fallback, account disable/expiry/quota, authentication, credential exclusion,
read-only preview behavior, retry, cancellation and expired-session messaging.
The new Center preview suite also passed with `GOWORK=off` against the existing
pinned Glivela module. Its adapter therefore has no unpublished API dependency;
the final pin is still required to ship the shared-encoder change together.

Browser acceptance used a disposable loopback-only synthetic host, the real
Glivela manager, and built-in CUA browser control (no separate Browser skill was
available). It exercised expansion, entry-configuration navigation, snapshot
source, zero results, a simulated 503 followed by successful refresh, and a 390px
dark iframe viewport. No relevant console errors or framework overlays were
observed. The iframe verifies CSS responsiveness, not touch-device behavior or
the production host's complete theme. The temporary entry files are removed
after acceptance and are not part of the feature.

Initial npm installation hit a registry connection reset. The dependency package
records in an existing checkout's lockfile were compared and were identical;
its installed dependencies were privately copied for this checkout. A subsequent
type check and the tests above passed. This does not replace the normal clean
dependency installation and required checks in CI.

Required PR CI, signed catalog integration and production acceptance are
separate gates. Local acceptance alone does not authorize publication or
production deployment; the two feature PRs do not update installed applications.
