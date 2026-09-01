# Installer Worker

`worker.js` is the source deployed as the `vastora-installer` Cloudflare Worker
behind `vastora.petauron.com`. Bind the private `petauron-downloads` R2 bucket as
`INSTALLER_ASSETS`. GitHub Release is the durable installer history. The release
workflow verifies its four draft assets, uploads the active candidate to R2,
atomically replaces `vastora/current.json`, verifies both public endpoints,
deletes every non-current object below `vastora/releases/`, verifies again, and
publishes the GitHub Release last. The Worker validates the active manifest and serves its three
fixed current-release paths with a one-minute cache, and exposes the same R2
objects through immutable `/releases/vVERSION/ASSET` paths for automatic Center
updates. A versioned path becomes visible only after the release workflow writes
its immutable activation manifest; merely staged objects cannot be downloaded.
Historical version paths stop working after a newer release is selected; the
GitHub Release attachments remain available as durable history. The bucket
remains private.

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
file, preserve the `OAUTH_SESSIONS` Durable Object binding, and verify the
`INSTALLER_ASSETS` R2 binding, all three public installer paths, and the OAuth
callback after every deployment.
