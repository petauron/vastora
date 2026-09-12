# Pulse

Vastora manages one global Pulse Service container and optional native Pulse
collectors. It does not embed Pulse in Center, migrate Komari history, or remove
existing Komari installations.

## Setup

1. Install **Pulse 监控主机** from the application store on one Docker-capable node.
   Supply the browser's exact HTTPS origin and a private administrator setup token.
   See [authentication configuration](../../../docs/pulse-collector-config.md).
2. Add a **private HTTPS** access point to its dashboard service. Prefer the
   Headscale network when collectors span Sites; every selected collector must
   resolve and reach this name. A LAN entry only works for nodes that can reach
   that LAN. HTTPS uses Vastora's existing certificate workflow.
3. Install **Pulse 探针** on each node to monitor, including the monitoring host
   itself if desired. Center automatically inherits the node name and Site group.
4. Use **打开监控** to open Pulse's dashboard and initialize the administrator at
   `/login` using the setup token. Subsequent access uses the Pulse login.

No enrollment token is entered or displayed in the UI. Center queues a fixed
`pulse-service enrollment create` operation on the monitoring host, encrypts the
short-lived result, and delivers it only to the selected collector. The collector
uses Pulse's own enrollment implementation and keeps its per-node credential
across upgrades and keep-data reinstalls. A failed preparation ends the install
with a retryable error rather than leaving it indefinitely pending.

## Access and runtime

- Service: pinned `v0.1.0-alpha.3` multi-architecture image, non-root UID 65532,
  private host port 18080 to container 8080, `vastora-pulse-data` volume.
- Collector: checksum-pinned upstream archive, exact executable extraction,
  dedicated unprivileged systemd account, no Docker, no inbound port. Original
  archive and license notices remain under `/opt/vastora/pulse-agent`.
- Current native release requires **Debian 12/13 or Ubuntu 24.04/26.04**. Vastora
  Agent still supports Ubuntu 22.04, but this Pulse binary does not; installation
  rejects that runtime before replacing files. No container fallback or TLS bypass.
- Dashboard and read APIs use Pulse's built-in authentication. Vastora's existing
  access policy is unchanged: direct public publication is blocked, and optional
  Internet dashboard access uses a Cloudflare Access-protected Tunnel. Collectors keep
  using their private HTTPS entry and do not go through browser authentication.
- Pulse defaults remain seven-day history, 90-second offline threshold, 100 nodes
  and 2 GiB database limit. These are Pulse's defaults, not new Vastora settings.
- Vastora does not configure or send a collection interval. New installations and
  upgrades use Pulse's own default; reporting controls remain in Pulse.

## Lifecycle

Service upgrades use Pulse's supported online backup before replacement; the new
Service performs forward-only schema migration. A failed upgrade never starts an
old image against the changed database. Uninstall collectors before the Service.

Collector keep-data uninstall preserves its local identity; delete-data uninstall
removes that credential, so the next installation registers a new Pulse node.
Pulse's historical records are not deleted by uninstalling a collector. Changing
the monitoring Service URL is intentionally not an automatic identity migration.

Center schema 71 extends the existing typed command queue while preserving queued
3x-ui operations and exclusion guards. The standard migration runner backs up
first, fails closed, and refuses downgrade.

Regression tests cover the command handoff, secret redaction, failed enrollment,
native lifecycle, safe archive selection, entry prerequisites, and migration.
They have been added but not run locally under this repository's verification policy.

Upstream: https://github.com/petauron/pulse/releases/tag/v0.1.0-alpha.3
