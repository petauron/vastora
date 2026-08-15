# Architecture

Vastora is a Go control plane with a React web console. The Master owns desired
state; Nodes own host access and the last successful application of that state.

```text
browser -> Master API -> encrypted SQLite
                         | signed catalog cache
                         | desired startup configuration
                         v
                    HTTPS long polling
                         v
Node Agent -> local encrypted SQLite -> Docker Compose -> application data
```

The Master does not receive application databases, logs, sessions, OAuth files,
or volume contents. A Node stores only the minimum encrypted startup bundle
needed to restart an already-applied application during a Master outage.

v0.1 has one Master and no election protocol. Losing the Master pauses changes;
it must not stop or erase existing applications.

## Backup and recovery

`vastora master backup` uses SQLite's `VACUUM INTO` to create a consistent
snapshot, archives it with `master.key`, and encrypts the archive using
scrypt-derived AES-256-GCM. Restore verifies the archive before writing and
refuses a non-empty destination. It is therefore safe to prepare a new VPS
without overwriting a running Master. The Node-enrollment slice will add the
control-plane CA to this same backup boundary.

## Current implementation boundary

The Master configuration, signed catalog cache, credentials, sessions, backup,
and local Node encrypted-state store are implemented. Transport enrollment,
job leasing, Compose rendering, registry-pull execution, and application
installation are deliberately not exposed until their complete vertical slices
are implemented and tested.

## Versioning

An app has one immutable `version` and images are referenced by content digest.
The catalog rejects a different manifest for an existing app version. There is
no revision field, historical marketplace, or automatic rollback.
