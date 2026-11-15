# Senlun runtime naming migration

The monitoring integration now lives in `internal/senulun`, with canonical
types, method names, source files, frontend components and exported helpers.
Glivela must consume the matching pinned frontend source commit.

The original application UUIDs, signed catalog IDs, task wire IDs/JSON fields,
released SQL schemas and verified backing-resource names are preserved. These
are ownership/history evidence, not runtime product aliases. Renaming them
without re-signing or explicit resource migration would disconnect installations
or queued work. No released migration, credential or monitoring history is
rewritten by this source refactor.

When a new verified Senlun recipe declares the canonical environment,
preparation converts inspected legacy Service environment assignments once,
retaining OAuth, private key paths, limits and custom settings. Conflicting
assignments or a custom database location require operator review before the
writer is stopped; no empty replacement is initialized. The old volume remains
attached to the same receipt while its mount moves to the canonical location.
The new Service explicitly backs up and migrates its database pair before
serving. Collector configuration conversion keeps the existing credential/state
directory and adds retained optional settings to the new generated environment.
Systemd preserves the same state directory identity and assigns its ownership
to the new recipe's service account.

CLI administration chooses the actual inspected entrypoint of the installed
signed image. Supporting backup of an old installed release is not a fallback:
an operation never retries a failed command with another binary. A new image
must expose the canonical executable; unknown identities fail closed.

Review this with the Senlun, catalog and Glivela PRs. This PR does not publish
a version, sign a catalog, change production, synchronize Agents or restart
services. Future rollout requires separate approval and verification of original
node IDs, settings, traffic/history and fresh reporting after managed upgrades.
