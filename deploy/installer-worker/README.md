# Installer redirect Worker

`worker.js` is the source deployed as the `vastora-installer` Cloudflare Worker
behind `vastora.petauron.com`. It selects the newest non-draft GitHub release
that contains the installer, bundle, and checksum, including prereleases. The
five-minute cache keeps GitHub API use low without coupling the public command
to GitHub's stable-only `releases/latest` endpoint.

The Worker contains no credentials. Keep the deployed source identical to this
file and verify all three public paths after every release.
