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

The Worker contains no credentials. Keep the deployed source identical to this
file and verify all three public installer paths plus the OAuth callback after
every release.
