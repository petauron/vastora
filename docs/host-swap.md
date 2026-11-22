# Host zram and swap optimization (MVP)

Related: #1389. This is an opt-in, per-node operation for small Linux `systemd`
KVM hosts. Upgrading Center or Agent, enrolling a node, or opening the node page
never modifies swap, zram, `/etc/fstab`, or `sysctl` automatically.

## Workflow

1. Open **Nodes → Manage → Host zram & swap optimization** on an enrolled Linux
   root Agent. Reading the sheet displays the last durable inspection record and
   does not queue host work automatically.
2. Click **Inspect host** to collect live host evidence (`MemTotal`,
   `MemAvailable`, active `/proc/swaps` devices, `/dev/zram0` disk size,
   compression algorithm and uncompressed logical bytes, `vm.swappiness`, root
   filesystem free space, virtualization kind, and `systemd` availability).
3. When the host is `eligible`, review the fixed tier summary and explicitly
   confirm **Apply fixed policy**. The Agent re-evaluates live host state and
   requires the exact inspected `revision` digest before mutating the host.
4. To revert an `applied` host, inspect current headroom and confirm **Restore
   initial state**. Restoration refuses to run when available RAM cannot safely
   absorb swapped pages (`MemAvailable <= zram_logical + swapfile_used + 64 MiB`).

## Fixed memory tiers and read-only boundaries

No arbitrary `sysctl` key, file path, shell command, device name, or custom swap
size is accepted. Tier selection uses `/proc/meminfo` `MemTotal` with standard
kernel-reserved memory tolerance (`896..1056 * tierMiB` KiB):

| Memory tier | Eligibility | Managed zram | Managed `/swapfile` | `vm.swappiness` |
| --- | --- | --- | --- | --- |
| `512 MiB` | Linux `systemd` KVM/QEMU | `256 MiB` `/dev/zram0` (`lz4`, priority `100`) | `512 MiB` `/swapfile` (`0600`, priority `-2`) | `60` |
| `1 GiB` | Linux `systemd` KVM/QEMU | `256 MiB` `/dev/zram0` (`lz4`, priority `100`) | `512 MiB` `/swapfile` (`0600`, priority `-2`) | `60` |
| `2 GiB` | Linux `systemd` KVM/QEMU | None | `512 MiB` `/swapfile` (`0600`, priority `-2`) | `10` |
| Other sizes (`>= 4 GiB`, `< 512 MiB`) | Read-only | Unchanged | Unchanged | Unchanged |

A host remains strictly `read_only` (`canApply = false`) when any of the
following holds before managed application:

- Virtualization is a container or non-KVM guest (for example `lxc`), or PID 1
  is not `systemd` (for example Alpine/OpenRC).
- Host RAM falls outside the `512 MiB`, `1 GiB`, and `2 GiB` tiers.
- Any active swap device, `/dev/zram0` configuration, `/swapfile`, `/etc/fstab`
  swap entry, or conflicting unit/drop-in already exists on the host.
- Kernel `zram` support is unavailable on a tier that requires `zram0`.
- Root filesystem available space is below `1.5 GiB` (`512 MiB` swapfile plus
  `1 GiB` safety margin).

## Owned host paths and recovery

All mutations serialize under `/var/lib/vastora/agent/host-swap.lock` and record
recovery state in `/var/lib/vastora/agent/host-swap.json` (`0600`) before
changing host files:

- `/var/lib/vastora/agent/host-swap.json`: records `stage`
  (`applying | applied | restoring | restored`), task ID, original
  `vm.swappiness`, and target tier policy.
- `/usr/local/sbin/vastora-zram` (`0755`) and
  `/etc/systemd/system/vastora-zram.service` (`0644`): installed only on the
  `512 MiB` and `1 GiB` tiers; loads `zram`, waits for `udevadm settle`,
  configures `lz4` and `disksize`, formats swap, and activates `/dev/zram0` at
  priority `100` across reboots.
- `/swapfile` (`0600`): allocated by writing explicit zero blocks (avoiding
  sparse-file holes rejected by `swapon`), formatted with `mkswap`, enabled at
  priority `-2`, and persisted via a single marked `/etc/fstab` block
  (`/swapfile none swap sw,pri=-2 0 0`).
- `/etc/sysctl.d/zz-petauron-swap.conf` (`0644`): persists `vm.swappiness` for
  the selected tier and applies `/proc/sys/vm/swappiness` with readback
  verification.

If an owned file or active swap device is modified externally after `apply`,
inspection reports `action_required` rather than overwriting administrator edits
or blindly reapplying. Agent decommission and `vastora agent uninstall` refuse
removal while managed host swap has not been restored, preventing `--delete-data`
from deleting `host-swap.json` while `/swapfile` or `vastora-zram.service`
remains active.

## API and persistence

- `GET /api/v1/agents/{id}/swap`: administrator session; returns the last durable
  task state and host swap view (`unconfigured` before first inspection).
- `POST /api/v1/agents/{id}/swap`: administrator session + CSRF; queues bounded
  `inspect | apply | restore` work (`apply` and `restore` require the current
  64-hex `revision`).
- Supported on both standard application nodes and `landingOnly` Linux root
  nodes. Uncertain or failed executions use the existing **Tasks** abandonment
  review flow (`abandon` only; never automatic or manual replay).
- Forward-only Center SQLite migration `134` (`00134_node_swap.sql`) adds
  `node_swap_tasks`. Database downgrade remains unsupported.

## Verification status

Unit and contract tests cover tier selection, revision-fenced task validation,
512 MiB / 1 GiB / 2 GiB host lifecycle simulation, `restore` memory-headroom
refusal, container and unmanaged-swap `read_only` guards, Agent removal guards,
Center SQLite migration `134`, OpenAPI parity, and confirmation-gated UI
controls. Isolated disposable KVM VM verification across reboot and live memory
pressure should be completed before production rollout; this document does not
claim real-host acceptance.
