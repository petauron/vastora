# Isolated Meridian UDP lab

Run only on an authorized Linux development machine with Docker, Go 1.26,
`ip`, `nsenter`, `timeout`, `tcpdump` and passwordless sudo for network namespace
setup/capture. It creates five temporary containers and one **internal** Docker
network. Nothing binds the machine's real TCP/UDP 443. Images are pinned by
digest; the subnet `192.168.240.0/24` must be unused by Docker.

From the repository root:

```sh
lab=$(mktemp -d /tmp/vastora-fullcone.XXXXXX)
go run scripts/meridian-fullcone-lab/generate.go "$lab"
cp scripts/meridian-fullcone-lab/*.py scripts/meridian-fullcone-lab/haproxy.cfg "$lab/"
sh scripts/meridian-fullcone-lab/run.sh "$lab"
tcpdump -nn -r "$lab/stun.pcap"
```

Preparation may run on another machine, then copy the prepared directory to
the Linux development machine and pass its absolute path to `run.sh`.
Each run generates disposable credentials and a short-lived certificate.
Do not commit generated files; keep the directory local and remove it after
reviewing the evidence. The lab makes fixtures readable by isolated test
containers. No real credentials or production reports belong in that directory.

`rendered.json` is the unchanged Meridian output, validated by the pinned
Xray binary. `server.json` adds only the fixture's exact private STUN addresses
and UDP ports to Xray's private-destination policy. Client configurations use
XUDP for VLESS and certificate verification for HY2.

HAProxy has its own bridge namespace and reaches Xray through the private
backend address. Xray shares only the namespace holder; HY2 reaches it directly.

The probe uses SOCKS5 UDP ASSOCIATE, STUN binding/CHANGE-REQUEST and
XOR-MAPPED-ADDRESS. It asserts ordinary, changed-IP/port and changed-port
responses, then checks destination-independent mapping within one association.
The script repeats both protocols after restarting Xray and HAProxy. It also
checks API isolation and unknown-SNI rejection; unknown SNI may take several
seconds to close while HAProxy exhausts its rejected backend retries.

The capture covers only the fixture peer namespace. Containers/network are
removed on exit; logs, generated fixtures and the capture remain in the
provided directory. On failure it waits at most for the 45-second capture
deadline before cleanup. Existing Docker networks and host firewall rules
are not changed. Image cache entries remain available for subsequent runs.

See [runtime scope and evidence limits](../../docs/meridian-host-runtime.md).
