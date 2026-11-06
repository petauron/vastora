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
