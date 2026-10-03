# Authenticated runtime reconstruction lab

This isolated Linux Docker lab validates real VLESS/REALITY client requests
before and after reconstructing the entry runtime on a different listen address.
It uses the current Meridian Go dependency's production artifact renderer,
digest-pinned Xray 26.7.28 and HAProxy, and disposable synthetic credentials.

The same saved clients run in both phases:

- Native client: a nonce-bearing HTTP request reaches the witness from the entry.
- Fixed-route client: the same request reaches it from the landing peer.
- Invalid client: an unconfigured UUID cannot make a successful witness request.

The witness checks the nonce and peer address, not just a process, socket, TLS
handshake or cached response. A failed positive request stops the lab. Only a
local socket readiness check is retried; client acceptance is not auto-retried.

## Run

Prepare a new disposable directory from the repository root:

```sh
lab=$(mktemp -d /tmp/meridian-recovery-lab.XXXXXX)
go run scripts/meridian-recovery-lab/generate.go "$lab"
cp scripts/meridian-recovery-lab/*.py scripts/meridian-recovery-lab/*.cfg "$lab/"
sh scripts/meridian-recovery-lab/run.sh "$lab"
```

Generation and execution may run on different machines; transfer the complete
prepared directory to the Linux Docker host first. Run with an outer timeout of
180 seconds. Container root may be user-mapped, so the runner permits reading
these **synthetic fixture files**. Never supply production credentials, exports,
certificates or state files as the prepared directory.

The bridge is internal, no host port is published, each container has one CPU,
128 MiB memory, a read-only filesystem and no added capabilities. Resource names
are unique and checked before ownership; cleanup removes only this run's owned
containers/network. Docker refuses a conflicting subnet. Failed runs retain
local synthetic logs in the fixture directory. Successful output contains only
phase, client class and acceptance flags.

## Evidence limits

The rendered configuration is retained unchanged for client/business identity;
the replacement fixture changes only the approved listen address. A fixture-only
freedom rule allows the synthetic private witness. Production destination
restrictions are unchanged. The landing uses an isolated Xray SOCKS peer: this
checks fixed-route selection, not the managed Dante installation or its private
identity/ACL lifecycle.

This is **not** the complete #759 reinstall drill. It does not invoke Center's
recovery command, reenroll Agent/Headscale, restore Pulse, migrate public DNS,
settle historical executions, persist an authenticated acceptance receipt or
release the recovery fence. Those still require their own end-to-end checks.
It covers VLESS/REALITY TCP only, not Hysteria or UDP acceptance. Running it must
not close #759 or mark a production node recovered.
