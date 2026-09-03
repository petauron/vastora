# Tailscale compatibility (#326)

## Installation pin and compatibility floor

`internal/tailscalehost/config.go` has two intentionally independent constants:

- `DefaultInstallVersion = "1.102.3"`: the stable APT package selected only when
  the host has no Tailscale client. This keeps fresh installations reproducible.
- `MinimumCompatibleVersion = "1.102.3"`: the conservative compatibility floor.
  Lowering it requires evidence for the required daemon and Headscale features;
  changing the installation pin does not automatically change this floor.

Existing installations are not upgraded, downgraded, reinstalled, or adopted by
this check. An unsupported existing version stops installation with the current
version, minimum version, reason, and an instruction to install a supported
stable package. The explicitly requested Headscale join still prepares
Vastora-owned privacy/resolver settings; it does not overwrite a user-authored
daemon configuration or turn an external installation into a managed one.

## Supported Agent host systems

The installer accepts Debian 12/13 and Ubuntu 22.04/24.04/26.04 on amd64 and
arm64. It reads the target server's `/etc/os-release` and uses `dpkg` to select
the native Agent binary before making any installation changes. Debian 11,
older Ubuntu versions, derivatives, and unlisted releases are rejected.

New Tailscale installations use the official stable repository for the exact
distribution and release: Debian `bookworm`/`trixie`, or Ubuntu
`jammy`/`noble`/`resolute`. They never reuse Ubuntu Noble's repository on another
release or distribution. The default installation pin and the compatibility
floor above remain unchanged; adding a supported OS does not authorize
replacing or downgrading an existing Tailscale installation.

The Center-hosted Docker installer uses the same OS and architecture policy,
handles both root and sudo users, and skips existing Docker installations.
Conflicting packages require operator review instead of automatic removal. See the
[Docker Debian instructions](https://docs.docker.com/engine/install/debian/),
[Docker Ubuntu instructions](https://docs.docker.com/engine/install/ubuntu/),
and [Tailscale stable package instructions](https://pkgs.tailscale.com/stable/).

## Shared policy

Installer preflight, fixed-endpoint reconciliation, and explicit legacy
adoption all call `tailscalehost.CheckCompatibility`. There is no shell-only
version comparison and no exact equality check against the installation pin.

Versions use numeric semantic-version ordering from the existing `x/mod/semver`
dependency. The supported format is a Tailscale 1.x stable branch (even minor),
at or above the floor. Valid build metadata and Tailscale's documented `-tHASH`
and `-gHASH` long-version stamps are supported. Arbitrary suffix stripping is
not allowed: prerelease/dev suffixes, odd-minor unstable branches, dirty build
metadata, future major versions, malformed or inconsistent output fail closed.
Version text is not provenance or certification of every future binary.

Before changing configuration, the check reads structured CLI version metadata,
the installed daemon's short and long versions, and its `--config` capability.
This preflight does not need a running daemon, so it cannot prevent recovery of
a stopped managed service. `vastora agent check-tailscale` exposes this read-only
preflight to the downloaded Agent installer.

After joining or restarting, `--require-running` also checks:

- the actual daemon version returned by the local API, not just the CLI;
- that the running and installed daemon release versions agree;
- an active systemd service and a `Running` backend;
- `TS_NO_LOGS_NO_SUPPORT=true` in the loaded systemd environment.

Fixed-endpoint reconciliation additionally checks the loaded configuration
path, fixed UDP port and listener. Its existing atomic writes and rollback
remain in place. An unsupported `alpha0` / `staticEndpoints` configuration must
fail service health verification and restore the previous files; it is never
silently omitted to accept a newer binary. No node identity, login state or
keys are reset. Existing verified control-endpoint and live DERP-map isolation
checks are retained; no official relay or log-upload fallback is introduced.

## Explicit adoption

A passing version check is not ownership proof. Legacy adoption still requires
the enrolled external-ownership state, Vastora Agent unit, exact managed
privacy override and applied marker, the managed Headscale hosts section, and
the complete historical APT installer command.

The historical `tailscale=1.102.3 tailscale-archive-keyring` command is retained
as evidence of the specific old installer being repaired, independently of
today's default pin and the currently installed compatible version. A normal
later package upgrade therefore does not invalidate the earlier evidence.
Unknown/manual install histories do not qualify. The operation is explicit;
an Agent restart failure restores the previous ownership record.

## Verification and remaining acceptance gate

Source regression cases cover numeric ordering, same-series and cross-stable
upgrades, build metadata, malformed/development versions, CLI/daemon mismatch,
missing configuration support, stopped-daemon recovery, missing privacy
settings, configuration rollback, and historical ownership proof. Existing
runtime privacy tests still reject unexpected DERP regions and unverified
Headscale endpoints. No local tests, builds or runtime checks were run while
implementing this change.

The higher-version strings in mocked tests, including `1.104.0`, are synthetic
policy inputs. They are **not** claims about published or tested packages.
At the 2026-09-03 upstream review, the newest published stable release was still
[`v1.102.3`](https://github.com/tailscale/tailscale/releases/tag/v1.102.3).

Issue #326 must remain open until a CI run exercises both the minimum and at
least one higher, actually published stable package against the bundled
Headscale. That acceptance run must cover install/join, fixed-endpoint apply
and rollback, privacy/DERP checks, preservation of external ownership, and an
upgrade retaining the node identity. Reject a prerelease or nonexistent matrix
version rather than substituting a synthetic version or lowering the floor.
Run this expensive matrix explicitly or in full CI, not on the alpha fast path.

Upstream references:

- [Daemon configuration and its alpha schema](https://tailscale.com/docs/reference/tailscaled/tailscaled-config-file).
- [Stable and unstable client version tracks](https://tailscale.com/docs/reference/tailscale-client-versions).
- [Structured version metadata](https://github.com/tailscale/tailscale/blob/v1.102.3/version/prop.go).
- [Daemon version output](https://github.com/tailscale/tailscale/blob/v1.102.3/version/print.go).
