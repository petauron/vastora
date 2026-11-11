# Alpine/OpenRC native runtime (MVP)

This source change supports Alpine 3.24 on Linux amd64/arm64 for the Vastora
Agent and portable native applications. The isolated acceptance guest uses
amd64; arm64 runtime acceptance has not been performed. Production migration
and publication are separate operations. Track delivery in Vastora #1182.

## Installation and service ownership

The installer detects the running init system. Alpine uses real OpenRC init
scripts, `rc-update` and `supervise-daemon`; no `systemctl` shim is installed.
The supported Agent launcher is `/usr/local/bin/vastora`. Its durable enrollment
and update state remain under the existing Agent data directory.

A managed update runs in a separate one-shot service, so stopping the Agent
does not stop its updater. Existing execution authorization, database recovery,
forward migration and explicit handling of interrupted updates remain in force.
A completed update removes its temporary service and protected update material.

Alpine requires ca-certificates, curl, iproute2 and OpenRC supervision tools.
Docker Worker bootstrap uses the distribution's docker/docker-openrc packages;
Glivela also requires nftables. For a fresh managed private network installation,
the installer uses the reviewed official static Tailscale 1.102.3 archive with
architecture-specific SHA-256 pins. It does not install an APK below the security
floor, enable an edge repository, install gcompat, or overwrite an external
Tailscale installation. Existing unsupported installations fail the compatibility
check before enrollment. Preserve the static installation receipt when resuming.

## Portable application contract

The Catalog SDK adds `native` executor version 2, with the existing declarative
nativeServices schema. `systemd` declarations remain systemd-only. Hosts advertise
only the init adapters they actually implement; this is not a runtime fallback.
A release must first publish the SDK, update the consumer dependency, and publish
appropriate signed application recipes/artifacts. These local changes do not
modify the existing signed catalog or automatically upgrade an installed app.

The Agent owns init scripts, process specifications, dedicated users and the
immutable installation inventory. Application configuration, credentials and
state remain application-owned and are retained across upgrades. OpenRC launchers
parse environment data rather than sourcing shell commands. They reconstruct
runtime credentials after every boot and wait for the pinned application to exec.
Logs are bounded reads from Alpine's normal syslog file, filtered by service tag.
A GNU-linked ELF is rejected before stopping a working Alpine application.

Glivela derives its OpenRC service identity from the local immutable inventory.
Its own controller authentication, process generations, worker ownership,
firewall, startup warmup and proxy acceptance remain independent of Vastora.
Senlun's Linux probe is built as a static musl executable and retains its existing
protocol and credential path. The Senlun Service release remains GNU/Linux;
this work does not claim an Alpine Service distribution or UI acceptance.

## Verification boundary

### No-TUN private management (follow-up #1214)

A running userspace Tailscale daemon can carry the Agent's TLS gRPC management
connection. Explicitly configure the host, rather than exporting proxy variables:

```json
{"socks5":"socks5://127.0.0.1:1055"}
```

Save this as `/etc/petauron/private-network.json`, owned by root, mode 0600,
in a root-owned directory that is not writable by other users. Start tailscaled
with `--tun=userspace-networking --socks5-server=127.0.0.1:1055`; retain its own
state and OpenRC service. Keep the certificate hostname in the HTTPS Center
URL. When private DNS is
not installed by userspace networking, pin that exact hostname to its verified
tailnet address in the host resolver configuration. The Agent accepts named
management destinations only when every resolved address belongs to the tailnet,
then pins the transport address while retaining normal TLS hostname/CA checks.
Literal Tailscale destinations also use this policy; public endpoints, application
proxy egress and ambient proxy environment variables do not. Invalid policy
fails closed. Glivela's paired controller/node build uses the same explicit
policy for literal tailnet endpoints while retaining mTLS validation.

Userspace networking does not assign these addresses to a kernel interface.
Do not advertise them as interface addresses or claim support for kernel-bound
landing listeners, subnet routing or UDP merely because management is online.
The no-TUN lab confirms an actually observed, firewalled LAN service address;
its application management connection still crosses the authenticated overlay.

Fresh OpenRC installs keep dedicated workers non-root. Protected parent
directories permit search, not listing; each root-owned process specification
is readable only by its own service group (0640). Durable environment and
credential files remain 0600. No compatibility shim or Docker is required.

Native shared-Caddy websites also use OpenRC supervision and the same protected
launcher. Their dedicated non-root account has only the bind-service capability;
bootstrap credentials are reconstructed under its private runtime directory,
and the Agent verifies its receipt, executable, specification, unit and state
owner before reusing it. The admin API remains a private Unix socket. Caddy uses
the existing 96 MiB Go memory target; this does not claim systemd's hard resource
limits on an OpenRC host. Existing systemd shared-Caddy resources are unchanged.

The isolated follow-up uses real Headscale enrollment, a separately signed lab
catalog, real Center task authorization/receipts and static release probes.
Its GNU Senlun backend uses a private loader directory, not an Alpine Service
package or a system musl replacement. Fixture HTTPS responses and same-guest
peers are synthetic; they do not establish commercial VPS or WAN acceptance.

### Previous #1182 acceptance

The dedicated Alpine LXC uses synthetic enrollment, independent synthetic
Glivela/Senlun identities and a pinned Caddy HTTPS target. No production account,
subscription, token or proxy route is imported. Fixture tooling is opt-in under
`tools/alpine-acceptance`, requires a root-only guest marker, and is not shipped.

The acceptance scope covers Agent installation and authorized managed update;
portable application installation, package-revision upgrade and guest reboot;
Glivela authenticated readiness and real VLESS/REALITY requests; and Senlun
credential preservation with fresh authenticated monitoring reports. The fixture
uses a real Senlun backend with a placeholder embedded page; no Web UI is tested.

The private-network prerequisite check uses an unauthenticated userspace daemon
in the guest. It validates version, service and privacy configuration, not a real
Headscale enrollment. No production private-network identity is copied.

LXC shares the PVE host kernel. Guest reboot proves OpenRC and runtime-directory
recovery, not a separate Alpine kernel, host swap behavior or a VPS reinstall.
Other host administration operations that require systemd/apt remain outside this
MVP. BreadCloud and other production hosts are unchanged.

## Existing systemd v2 installations

An explicitly authorized application upgrade can migrate an owned systemd v2
receipt to the portable native v2 contract on the same systemd host. The Agent
requires unchanged capabilities and verifies the existing files, users, units
and state against the target plan before preparing the upgrade. The ordinary
apply and health lifecycle persists the new receipt; failed preflight leaves
the installed receipt untouched. No service is re-enrolled, no data directory
is recreated, and no OpenRC adapter is substituted. Reverse migration, v1
conversion, unreviewed partial execution and other executor changes fail closed.
