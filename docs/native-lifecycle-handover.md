# Backup-bound native installation handover

This is the installation prerequisite for Glivela's replacement of the existing
proxy implementation, not a data-cleanup or general recovery refactor. The
Glivela controller remains Docker-based; its node and Xray services use systemd.
The existing shared HAProxy and unrelated services remain unchanged.

The ordinary signed package and sealed execution checks still apply. Restricted
nodes permit only explicitly reviewed systemd v2 install/upgrade/uninstall tasks,
with `native-services` and optional `root`. An executor is constructed only for
that individual task; Docker, gateways, tunnels and host-path grants remain
unavailable. First installation requires explicit backup confirmation.

`agent stage-native-package` prepares stopped native resources from reviewed
inputs and an exact existing resource receipt. The subsequent ordinary install
binds `replaceApplicationId` and `sourceReceiptSha256` to that staged inventory.
Only a matching result commits the original application registration to the
new package. Existing configuration, identities and private recovery backups
are retained; an uncertain result is not reported as successful.

The explicit same-node/site service handover preserves service/publication IDs,
public hostnames, paths, TLS and access boundaries. It uses existing object IDs
and current receipt fingerprints, never a caller-selected backend. Binding
updates remain pending real publication verification. It neither fabricates
old task success nor deletes retained history. See [service handover](service-handover.md).

Before runtime cutover, the deployment owner must back up configuration and
databases, freeze the old writer, import the frozen data through Glivela, and
verify the new controller/node identity and real proxy routes. Source CI is not
production acceptance. This change does not authorize production deployment or
cleanup of old installations, databases, histories or backups.
