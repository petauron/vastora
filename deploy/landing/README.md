# Native Dante bundle

The landing server uses Dante 1.4.4. This product includes software developed by
Inferno Nettverk A/S, Norway.

`Dockerfile` builds the original upstream source against Ubuntu 22.04's glibc
baseline for amd64 and arm64. The source SHA-256 is pinned to the digest published
by [Dante upstream](https://www.inet.no/dante/download.html). It emits only the
compressed server executable, checksum and upstream license/acknowledgement.
The build container is release infrastructure, not a landing runtime container.

The same executable baseline is intended for the Agent's supported Debian 12/13
and Ubuntu 22.04/24.04/26.04 systems. PAM, GSSAPI and libwrap are disabled; source
restrictions remain in Dante configuration and the Agent's firewall policy.

Center and Agent Dockerfiles embed the matching executable and notices in the Go
binary. Provisioning verifies the ELF architecture, extracts it into Agent-owned
files, and tracks their hashes in the host journal. Source-only Go builds do not
include the artifact and explicitly refuse landing provisioning. They do not
silently install a different system package.

CI uses this release-built executable for configuration and TCP/UDP roundtrips,
then installs and removes the native service under real systemd on its disposable
Linux runner. These passed for commit `fef5bca` in
[run 34216052874](https://github.com/petauron/vastora/actions/runs/34216052874).
No local or production verification has been performed. The CI host lifecycle
check is not a runtime matrix across every supported distribution and architecture.
