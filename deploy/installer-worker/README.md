# Installer redirect Worker

`worker.js` is the source deployed as the `vastora-installer` Cloudflare Worker
behind `vastora.petauron.com`. It selects the newest non-draft GitHub release
named by the repository's `version.txt`, verifies that the installer, bundle,
and checksum are all public, and then caches the selection for one minute. A
seven-day last-known-good selection keeps installations working while a new
release is being assembled or GitHub is temporarily unavailable. This avoids
GitHub's anonymous API rate limit without coupling prereleases to the
stable-only `releases/latest` endpoint.

The Worker also relays the short-lived Cloudflare OAuth authorization code from
the fixed public callback to the private Center that started the login. It never
receives the PKCE verifier, access token, or refresh token. Configure a Durable
Object binding named `OAUTH_SESSIONS` for the exported `OAuthSession` class and
add the class with a `new_sqlite_classes` migration on the first deployment. Each
authorization result is returned once and is deleted after ten minutes.

`GET /network/public-address` returns the caller address from Cloudflare's
trusted connection metadata. Center uses it only as an automatic cloud-NAT
candidate during first-run setup; locally assigned addresses remain the source
of truth for direct-public node capability.

`POST /network/verify-public-entry` performs the corresponding pre-install
inbound check. The privileged deployment helper temporarily listens on the
selected host address on TCP ports 80 and 443. The Worker connects to the
caller's trusted `CF-Connecting-IP`, completes a random challenge on both
ports, and Center immediately stops the listeners. No hostname, Caddy, or
Headscale installation is required for this check. A successful result proves
only that public Web traffic can reach both required ports; it is not a claim
that the host has unrestricted one-to-one NAT for every port or protocol.

The Worker contains no credentials. Keep the deployed source identical to this
file and verify all three public installer paths plus the OAuth callback after
every release.
