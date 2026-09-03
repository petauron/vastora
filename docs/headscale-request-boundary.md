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

The original GitHub alerts **#2** and **#9** referenced older default-branch
commits (`8b472233` and `e16e8671`). PR #330 introduced the fixed request
origins, constrained dialing, redirect rejection, finite health/API path
selection, and empty-query/fragment and path-escape coverage described above.

A complete default-branch CodeQL run
[`33775535074`](https://github.com/petauron/vastora/actions/runs/33775535074)
then analyzed commit `49825cf`. GitHub automatically marked alert #9 fixed.
Alert #2 belonged only to the retired `analyze/language:go` configuration, so it
was recorded as mitigated with the current scan evidence. It was not classified
as a false positive because the historical implementation lacked the current
destination and redirect controls.

CodeQL runs on every push to `main` in addition to pull requests, the weekly
schedule, and manual dispatch. This ensures a merged fix advances the
default-branch analysis that owns security-alert state instead of leaving that
state behind the pull-request checks.

Regression cases live in `internal/center/network_integrations_test.go` and
`internal/deployer/config_test.go`. They cover fixed origins, rejected redirects,
unauthorized origins, user information, path escapes, query encoding, and empty
query/fragment delimiters.
