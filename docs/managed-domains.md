# Managed application domains

Vastora owns DNS allocation and DNS-provider credentials. Independent applications
consume the non-secret resource through their own authenticated management APIs;
they do not read the Center database or retain a Center administrator session.
This contract does not move application accounts, subscriptions or traffic ledgers.

## Administrator provisioning

`POST /api/v1/managed-domains` accepts `applicationId`, a stable application-scoped
`key`, and the node's confirmed public `address`. An empty `prefix` creates a
random ingress label. A two-to-eight-letter lowercase `prefix` creates a stable
region label such as `us-abcdefghij.example.com`. Names are persisted and reused
on retries, restarts and upgrades; they are not regenerated on every request.

An optional `hostname` adopts a previous domain only when the matching Cloudflare
record belongs to this application's retained publication. With no explicit
hostname or prefix, a retained long random publication is preferred. A previous
address may be supplied as `expectedRecordAddress` when a verified machine
migration requires updating that exact owned record. Foreign records and records
with conflicting owners are refused, not overwritten.

Address changes require `expectedRevision`; an existing resource cannot silently
switch between A and AAAA. Records are DNS-only. `ready` means the DNS provider
accepted the desired revision, not that recursive DNS has propagated or that the
application works. Readback is available at `GET /api/v1/managed-domains/{id}`;
list resources with `GET /api/v1/managed-domains?applicationId=…`.

Pass the returned non-secret resource to Glivela's authenticated `advertise-host`
operation as `resource`, with its `entryId`, `action: apply` and exact
`expectedRevision`. Glivela verifies the current and new ingress addresses match,
keeps SNI separate, and retains its own endpoint revisions and authorization.
Provider credentials never cross that boundary. Plain explicit host provisioning
remains available for independent installations using a different DNS manager.

## Ingress and SNI are separate

Use an independent random ingress domain for the subscription's server address.
Use another persisted region-plus-random resource for a node-local website/SNI.
For example, the ingress may be `abcdefghijklmnopqrstuvwxyz.example.com`, while
the same machine's SNI is `jp-abcdefghij.example.com`. Do not substitute a website
hostname for the ingress merely because both resolve to the same machine.

Provision and verify the managed website and certificate through the existing
[node website workflow](node-site-certificates.md) before applying a REALITY SNI
change. Retain the previous website and recovery state until acceptance. Preserve
the ingress address family, listener port, credentials, quota and cumulative
traffic. Migrate one entry at a time and verify its rendered subscription and real
proxy access, including related chains. Clients with cached parameters must
refresh their subscription after a SNI change.

## Ownership and removal

Adopting a publication transfers only DNS lifecycle to the resource. Its route
remains registered; stopping that old route does not delete the adopted record.
Application/node deletion leaves a cleanup tombstone. Cleanup requires the exact
managed record identity, name, type, content and ownership marker; foreign changes
require administrator review. Explicit `DELETE /api/v1/managed-domains/{id}` also
refuses active publication references. Verify external application consumers are
retired before requesting deletion.

The maintenance worker retries provider work with bounded backoff. It never probes
application traffic, withdraws a running subscription, or changes protocol keys.
This feature uses schema 131 and the normal forward-only migration/backup process.
Implementation and provider readback alone do not establish a deployed migration.
