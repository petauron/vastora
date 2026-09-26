# Independent catalog release and maintenance cutover

[petauron/catalog](https://github.com/petauron/catalog) owns authoritative recipes,
source registration, protocol and publication. Vastora retains consumer tests
and independently reviewed public trust in `catalog/trust/`. An application-only
catalog release does not build, commit to or release Vastora, and never upgrades
A1 or another installed instance automatically.

Current status: implementation and isolated testing are in progress. The new
publisher's production variable and migration-acceptance record remain disabled.
Source/tests are not proof of production migration, live publication, no-restart
adoption or an application-data restore drill.

| Change | Owner | Running applications |
| --- | --- | --- |
| Ordinary Docker/systemd app or recipe revision | Catalog PR/CI | Unchanged until explicit install/upgrade |
| Approved version of a registered app | App notification then independent catalog verification/signing | New available version only |
| Unsupported runtime or product integration | Vastora program change and compatible recipe | Coordinated Center/Agent upgrade |
| Install/configure/upgrade/backup/restore/uninstall | Authenticated Center action | Selected authorized task only |

See the independent [publishing guide](https://github.com/petauron/catalog/blob/main/docs/PUBLISHING.md)
for GitHub App setup, provenance verification, immutable ledgers, exact-byte
retries, seven-day signatures and daily renewal below 48 hours.
Ordinary updates do not modify protected `main` or open Vastora PRs. Routine
signing uses protected catalog `main` and its protected environment without
per-release approval; first registration, permissions and trust changes still
require review. Root private keys never enter CI.

Origin stays `https://downloads.petauron.com/vastora/catalog/`, identity
`vastora-official`, channel `stable`. Immutable objects precede conditional
`timestamp.json` activation and public verification. Preserve other projects'
objects and shared credentials. Serial publication preserves pending records;
automatic runs cannot skip, overwrite or re-sign them.

## Maintenance sequence

1. Finish implementation, publish/pin the immutable module, pass acceptance on
   data copies and review branches/PRs. A local workspace replacement is not a
   releasable dependency.
2. Freeze old catalog writes and new app changes. Resolve unfinished/unknown
   executions through existing disposition controls. Pausing claims alone does
   not stop an already authorized executor.
3. Independently back up Center database/keys, every Agent state/key set and all
   app volumes/state. A Center backup cannot replace app data backups. Rehearse
   restoration on copies.
4. During the freeze, re-export every historical catalog release/draft, original
   attachment and digest. Import original bytes, commits and signatures, retain
   gaps/reservations, and never reconstruct history from CDN contents.
5. Upgrade Center/Agents together. Forward-only migration marks historical
   resource records `pending`; it does not reinstall/reconfigure apps, trigger
   runtime-generation restarts or substitute current recipes for old manifests.
6. Record container ID/StartedAt, systemd MainPID/InvocationID, volume names,
   configuration hashes, ports/private addresses, ownership and pairing state.
   After backups, call `POST /api/v1/applications/{id}/adopt` with
   `{"backupsConfirmed":true}`. A `202 {"taskId":"..."}` response means queued,
   not complete; observe the task/application until `adoptionState=ready`.
7. Adoption inspects the historical manifest and actual resources, recording
   revision `0` and the original digest. It must not pull images, rebuild/restart
   containers/units, change privileges, migrate databases or replace credentials.
   Compare before/after runtime evidence. Unknown ownership is `blocked`, not
   permission for a repair reinstall.
8. After acceptance, retire the old publisher and transfer authority with exactly
   one writer. Keep download paths, roots, source identity, channel, TUF state
   and rollback high-water marks. Enable both production gates and publish
   schema 4 at a strictly higher catalog revision.
9. Refresh Center and verify visibility with no app task/restart. Existing Pulse
   `v0.1.0-alpha.5` can use its original tag/SHA/release run; no re-release.
10. Exit maintenance only after acceptance. Failures leave maintenance in force.
    Program rollback requires matching backup restoration; never run old code
    against newly migrated databases.

3x-ui-to-Meridian business migration is separate; adoption does not perform it
or authorize an app upgrade. Historical manifests are audit evidence only.

## Recovery and completion evidence

For adopted schema 4 instances,
`POST /api/v1/applications/{id}/maintenance` accepts
`{"action":"logs"}`, `{"action":"backup"}`, or
`{"action":"restore","backupId":"<recorded ID>"}` and returns a task ID.
Read `GET /api/v1/applications/{id}/maintenance/{taskId}` for state, bounded
redacted logs, backup ID and `reconciliationRequired`. No request accepts an
arbitrary path/command; responses do not expose resource receipts or backup
paths. Restoration is limited to the same recorded package version. External
bind-mounted Docker data cannot be automatically restored; use a reviewed
external recovery process. Queuing a task does not establish successful backup
or restoration, and uncertain results must not be replayed automatically.
`GET /api/v1/applications/{id}/backups` lists recorded IDs, package versions,
creation times, logical resource names and `restorable` status without exposing
host paths. A selectable backup is not proof of a successful recovery drill.
Each resource archive is bounded to 512 MiB and rejects links/special files;
larger datasets require an independently reviewed backup process. Docker
restores keep old named volumes and mount restored copies in replacement
containers. Logs are limited to 200 lines and 64 KiB per resource, with known
delivered secret values redacted. These limits are not a guarantee that an
application never prints an unknown secret in its own logs.

Upgrade-time executor backups do not replace pre-cutover backups. Validate
failed upgrades/restores on copies, proving old programs cannot touch new data.
Default uninstall must retain persistent data. Do not resolve uncertainty by
deleting receipts, rewriting ownership labels or resetting trusted revisions.

Agent removal without explicit data deletion also preserves non-empty Agent
state in place: application bind mounts, package receipts/backups, and database
recovery keys may share that directory. Only the completed host-install ownership
record is removed. Preserve directory permissions and include this retained state
in recovery handling; it is not safe to delete merely because the Agent service
and executable are gone. Unknown entries are retained rather than guessed to be
disposable.

Before production signoff, retain evidence for an application never named in
Vastora source with each runtime/architecture; privilege rejection, secret-safe
output, ownership/path defenses, duplicate/old notifications, interrupted upload,
exact pending retries, expiry/rollback, no-restart adoption, and a complete
release-to-visible-version flow without a Vastora code change.
