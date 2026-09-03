# Headscale request-forgery review (#304)

## Current boundary

The Center accepts an external Headscale origin only when it exactly matches an
operator-configured `--headscale-allowed-url`. Built-in Headscale can only be
configured from the privileged deployment helper's installation result. Origins
must be HTTPS and cannot contain user information, paths, query delimiters or
fragments, including empty `?` and `#` suffixes.

The Center selects one of two constant API URLs under
`https://headscale-api.vastora.invalid`. User names are encoded as query values or
JSON, not interpolated into the origin. A new transport dials only the authorized
Headscale destination, uses that origin's TLS server name and HTTP Host, disables
proxy inheritance, and does not inherit alternate TLS dialing hooks. Redirects
are not followed, so bearer credentials are not forwarded to another authority.

The deployer's gateway health request selects a constant `/health` or `/healthz`
URL under `https://local-gateway.vastora.invalid`. Its transport always connects
to the managed Caddy Docker alias and the caller's internally selected gateway
port. The configured public name supplies TLS SNI and HTTP Host only. Redirects
are rejected. No public name is resolved to choose the network destination.

This boundary does not stop an operator from deliberately authorizing a private
external Headscale instance. That is an explicit host-operator capability, not a
target chosen by an unauthenticated request or a browser-supplied URL.

## Alert disposition

At the 2026-09-03 read-only review, GitHub alerts **#2** and **#9** were still open,
but their latest instances referenced older commits (`8b472233` and `e16e8671`).
The current source already contained fixed request origins, constrained dialing,
and redirect rejection. This follow-up restricts the health/API path selection
to finite constant URLs and adds empty-query/fragment and path-escape coverage.

The old deployer code did not reject redirects, so an old alert must not be
blanket-dismissed as a false positive just because the current code is hardened.
After merge, run CodeQL against the new default-branch commit and inspect both
alert instances, including their analysis categories. A green workflow alone
does not establish that an old category's alerts have been resolved. Do not
dismiss or close them without that evidence.

Regression cases live in `internal/center/network_integrations_test.go` and
`internal/deployer/config_test.go`. They cover fixed origins, rejected redirects,
unauthorized origins, user information, path escapes, query encoding, and empty
query/fragment delimiters. No local tests or CodeQL runs were executed for this
review; GitHub alert closure remains a post-merge verification step.
