# Vastora service backups

Settings → Vastora backups configures encrypted restic snapshots for services
managed by Vastora on the Center host. Independent deployments are outside this
feature. It does not capture the entire host or back up remote Agent hosts.
The Center-only encrypted export remains a separate export.

## Scope and setup

Install `python3`, `restic`, `flock`, and the Docker CLI from the host distribution.
The release bundle contains `host-backup.py` and `install-backup-service.sh`.
Upgrading Center does not enable a schedule or stop any services.

Create `/opt/vastora/center/.host-backup/scope.json` (root owned, mode 0600):

```json
{
  "reviewed": true,
  "containers": ["vastora-center-1", "vastora-deployer-1"],
  "includeAgent": true,
  "excludes": []
}
```

Expand the example to include every required Vastora-managed application and
infrastructure container on this host, including normally stopped containers.
Each container must carry `io.vastora.managed=true`. Names alone do not establish
ownership; independent containers are rejected even if manually added to the list.

Sources are limited to the Center installation directory and selected containers'
persistent mounts. The local Vastora Agent is included when enabled and its unit
ownership, command and data directory can be verified. Only that Agent's unit
file and data directory are added. An overridden unit requires separate review.
The runner does not scan all of `/etc`, Compose project directories, or arbitrary
native services. Shared storage with a container outside the reviewed list blocks
the backup before stopping services. No arbitrary source/unit configuration is
supported.

Use explicit exclusions for old backup directories. Docker images, build caches
and runtime sockets are not backup data. Applications must keep persistent data
in mounted storage; writable container layers cannot be recovered from an image.
Place the repository outside all sources (default: `/srv/vastora-backups`).

Run `install-backup-service.sh --install-dir /opt/vastora/center`. Choose and
externally retain a password in Settings, save settings, review coverage, then
run a manual backup during a maintenance window. Scheduling is disabled by default.
Independent services will not be stopped or included.

## Failure and recovery

The host runner writes a restart journal before stopping an included running
service, then restarts those services on success or failure. After interruption
or reboot, it recovers pending services before another backup. Failed recovery
retains the journal. Updates and uninstall share its lock.

Nonzero restic exit codes, including partial reads, fail the backup. Retention
runs after snapshot success, service restart and repository metadata checking.
Only `vastora-host` snapshots for this host in this mechanism's repository are
pruned. Legacy backup files are not imported or deleted. A metadata check is not
a restore test.

Copy the entire repository off-host and retain the password independently.
Restore with `restic restore SNAPSHOT --target DIR` into an isolated directory.
The encrypted snapshot's `.host-backup/manifest.json` records included container
definitions and image references. Treat it and restored application configuration
as secrets. Center database downgrade remains unsupported.

Offsite storage is undecided; this release does not upload backups. Losing the
Oracle disk can still lose all local snapshots.
