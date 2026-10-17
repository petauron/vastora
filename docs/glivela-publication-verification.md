# Glivela public-entry verification

A published entry is ready only after a real protocol client completes HTTPS and
observes the expected public exit IP using the saved native subscription identity.
This is separate from node heartbeat and landing-route health.

## Network placement

The Center calls the Deployer through its restricted Unix socket. The Deployer
runs on the runtime bridge; `127.0.0.1` there does not address the Docker host.

Concurrent checks wait for one available slot within their request deadline.
Queue contention is not reported as a failed connectivity observation.

For each serialized check:

1. Create a labelled private bridge and a temporary pinned Xray client. Feed the
   subscription configuration on stdin; bind SOCKS only to namespace loopback.
2. Start a bounded helper using the installed immutable Vastora image and Docker
   `container:<client ID>` networking. No ports are published; the helper has no
   Docker socket, host mounts, credentials, or privileged capabilities.
3. Retry SOCKS readiness only, then run the existing authenticated HTTPS and exact
   expected-exit check once. Never fall back to a direct request.
4. Require helper exit status zero and confirmed ownership-checked cleanup before
   reporting success. Remove the helper before the client and its network. Use
   bounded tmpfs for the image's data-volume path to avoid anonymous data volumes.

The host Agent's recovery and native-egress probes still use a Docker-assigned
host-loopback port. Those callers actually run in the host network namespace.

A failed check must remain failed. Do not rewrite publication status in the
production database or substitute a heartbeat for authenticated traffic proof.

Reference: https://docs.docker.com/engine/network/#container-networks

## Exit evidence freshness

Agent public-egress observations refresh every five minutes. The original
observation timestamp is retained while cached, and failed refreshes retry no
more than once per minute. Center still rejects evidence older than ten minutes;
a heartbeat alone never renews the timestamp or constitutes exit proof.
