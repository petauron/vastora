# Vastora

Vastora is a self-hosted control plane for deploying containerized applications
to a Master host and passive Node Agents. It is designed for private VPS fleets:
the Master stores desired startup configuration, while every Node keeps its last
successfully applied encrypted configuration so existing applications can restart
when the Master is unavailable.

> **Pre-alpha.** The repository is intentionally published only with working,
> tested building blocks. Do not use it to manage production workloads yet.

## What is implemented now

- A Go `vastora` CLI with Master initialization, Master HTTP API, Node local
  encrypted state, and catalog signing/validation commands.
- An authenticated Master setup flow with a one-time bootstrap token and
  Argon2id password hashing.
- AES-256-GCM encrypted secrets in SQLite, multi-source catalog persistence,
  Ed25519 signed catalog verification, and a bilingual React catalog console.
- A signed CPA application package whose images are pinned by digest.

## Development

Requirements: Go 1.26.6, Node.js 24.19, and npm.

```sh
make bootstrap
make check
make security-check
```

Start a local control plane after the checks pass:

```sh
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora master init --data-dir .vastora/master
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora master serve --data-dir .vastora/master --listen 127.0.0.1:8080
```

The first command prints a one-time bootstrap token. Keep it out of shell
history and use it once in the web setup screen. In another terminal, start the
web development server with `cd web && npm run dev`.

Create an encrypted control-plane backup with a password stored in a local
`0600` file, then restore only into a new empty state directory:

```sh
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora master backup --data-dir .vastora/master --output master.vastora --password-file ./backup-password
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora master restore --input master.vastora --data-dir .vastora/restored-master --password-file ./backup-password
```

The Dockerfiles build an unprivileged Master image with the compiled web UI and
a separate Node image. They intentionally do not provide an insecure default
command: a network-reachable Master must be started with its TLS certificate
and key.

## Security model

- The Master never mounts a Docker socket.
- Catalog source content is accepted only after Ed25519 signature verification.
- Registry credentials and catalog Bearer tokens are separate from catalogs and
  stored encrypted.
- Application runtime data is Node-local and never uploaded as configuration.
- The current implementation is a foundation: Node enrollment, Docker
  deployment execution, control-plane CA management, and app installation are
  tracked for v0.1 and must be complete before the first stable release.

Read [the architecture](docs/architecture.md), [catalog format](docs/catalog.md),
and [threat model](docs/threat-model.md) before contributing.

## License

Apache-2.0. See [LICENSE](LICENSE).
