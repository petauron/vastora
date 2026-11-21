# Vastora-managed service backups

## Scope

Back up only Vastora-managed services on the Center host: Center infrastructure,
managed applications and the verified local Vastora Agent. Independent applications,
other native services and remote hosts are outside scope. Container ownership must
be established by `io.vastora.managed=true`, never inferred from a name.

The first delivery uses a local restic repository. The administrator configures
its directory, schedule, and retention from Settings and can start a backup and
view its outcome. Offsite storage is not configured yet. The UI must identify
local-only protection explicitly. An encryption password must be retained away
from the host before enabling the schedule.

## Execution contract

- Run on the host independently of Center and Docker containers, using systemd.
- Verify ownership of the reviewed containers and their persistent mounts; record immutable image
  identifiers and container definitions in the encrypted snapshot.
- Include only the verified local Vastora Agent unit and data directory.
  Do not sweep `/etc`, Compose project directories or arbitrary native services.
- Reject source overlap with any container outside the reviewed scope.
- Stop only the inventoried running services for a consistent snapshot. Persist
  the restart journal before stopping each service. Restart in dependency order
  on success, failure, interruption, and after reboot before accepting another run.
- Prevent overlap with another backup and with Center updates. A pending update
  must not replace the runner during a backup.
- Reject repositories inside any source path, source paths inside a repository,
  symlink destinations, and paths overlapping operating-system/runtime storage.
- Use restic encryption, compression, snapshots, and retention instead of a new
  archive/encryption format. A failed or partial backup is not successful.
- Apply retention only after a complete backup and service recovery. Never
  silently delete historical backups outside this mechanism.
- Exclude known historical backup directories, Docker image/build caches,
  sockets, and test/rehearsal artifacts using explicit exclusions. Do not exclude
  arbitrary application files merely because their names contain `backup`.

## UI

Settings → Backups:

1. Coverage and local/offsite status; services requiring a pause.
2. Directory, UTC run time, interval in days, and retained successful snapshots.
3. Manual backup, last result, snapshot history with sizes and errors.
4. Password setup and explanation that host loss requires an external password
   copy AND an off-host repository copy.

Saving a schedule is separate from running a backup. Defaults are disabled,
daily at 03:00 UTC, retaining seven successful snapshots. Do not imply that a
snapshot is restore-tested; restore verification is a distinct result.

## Release acceptance

- Unit coverage for scheduling, path validation, retention scope, journal
  recovery, and API authentication/CSRF.
- Isolated integration exercise: two services with persistent data, a failed
  backup, process interruption, service restart, and restore of the snapshot.
- Existing repository CI; no proactive local builds/tests.
- A1: review real inventory, install the host runner, configure repository and
  externally retained password, explicitly enable, then verify one snapshot and
  service recovery. Deployment and restore-test results must be reported apart.

## Remaining choices

Offsite destination is undecided. Do not create storage accounts, upload secrets,
or describe local backups as protection against loss of the Oracle host.

## Implementation status

Host runner, settings API and Settings card are present on the development branch.
No local tests/builds or production execution have been performed. Before release,
CI and an explicitly authorized isolated consistency/recovery exercise are required.
The Vastora service inventory and optional runner installation are intentionally separate from
an ordinary Center upgrade; upgrading must not introduce service downtime on a timer.
