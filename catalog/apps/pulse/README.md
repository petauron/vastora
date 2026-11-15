# Senlun

Senlun is an independent application. It owns administrator accounts, probe
enrollment, monitoring configuration, themes and monitoring data. Vastora manages
package installation, upgrades, runtime resources and access entries; it is not
required to run a Senlun Service or Agent installed outside Vastora.

## Install

1. Install **Senlun 监控主机** on a Docker-capable node. Set `public_url` to the
   exact HTTPS browser origin. Initialize the administrator at Senlun's `/login`
   page through a private entry before exposing the dashboard. There is no
   Vastora-generated administrator setup token.
2. In Senlun, create an Agent enrollment token.
3. Install **Senlun 探针**, explicitly supplying its HTTPS Service URL, node name,
   optional group and enrollment token. The Service may be installed elsewhere,
   including outside Vastora. Center delivers the token as an encrypted package
   secret and does not execute enrollment commands on the Service.

Independent Service installations on different hosts are allowed. Each Agent
chooses its own Service. The usual one-instance-per-package-per-host rule remains.
For installation without Vastora, see the
[Senlun standalone guide](https://github.com/petauron/senlun/blob/main/docs/STANDALONE.md).

## Access and runtime

- Service: checksum-pinned container image, private bind and persistent data
  volume. Senlun handles its own login and request authentication.
- Agent: checksum-verified native executable, unprivileged systemd user,
  persistent identity, no inbound listening port. The current native package
  requires Debian 12/13 or Ubuntu 24.04/26.04; unsupported hosts fail before
  replacing files. There is no TLS bypass or container fallback.
- Existing Vastora access policy remains unchanged: direct public publication is
  blocked; an Internet dashboard entry uses an Access-protected Tunnel.
  The Agent's HTTPS API endpoint must not require interactive browser login.
- Optional `trusted_proxy_ip` is the exact immediate reverse-proxy peer, not a
  wildcard or a client address. Leave it empty unless the proxy topology is known.
- Collection intervals, probe targets and monitoring policies belong to Senlun.
  Vastora does not override them.

## Lifecycle and historical installations

Upgrades preserve the Service volume and each Agent's identity and credentials;
they do not reenroll probes. Service upgrades retain the existing backup and
forward-only database migration protections. Never start an older program against
a migrated database; restoration needs its matching backup.

Uninstall preserves data unless deletion is explicitly requested. Removing the
Service does not uninstall Agents or move their identities to another Service.
New independently enrolled Agents use data restoration, not Center-managed
credential rotation, during host recovery.

Historical package IDs (`pulse`, `pulse-agent`), container names, unit names,
paths and installation receipts are resource ownership identifiers, not runtime
dependencies. This change does not rename or recreate them. Historical queued
operations and their recovery evidence remain readable; finish pending operations
before updating the manager. No new automatic Service enrollment is queued.

See [configuration](../../../docs/senulun-collector-config.md) for individual fields.
