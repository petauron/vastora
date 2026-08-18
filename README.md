# Vastora

Vastora is a self-hosted desired-state control plane for Docker applications
across multiple Sites and Nodes. The Center stores intent and orchestration
state; one Agent binary is the only node-side executor for Docker and an
optional local Caddy Gateway. Existing applications and applied routes continue
when the Center is unavailable.

> **Pre-alpha.** The repository is intentionally published only with working,
> tested building blocks. Do not use it to manage production workloads yet.

## What is implemented now

- A Go `vastora` CLI with a Center HTTP API, one-time Agent enrollment,
  authenticated Agent heartbeats, Agent-local encrypted credentials, and catalog
  signing/validation commands.
- Browser-based first-administrator setup with a username, Argon2id password
  hashing, and authenticated sessions.
- AES-256-GCM encrypted secrets in SQLite, multi-source catalog persistence,
  Ed25519 signed catalog verification, and a bilingual React catalog console.
- Organization → Site → Node topology, typed Network Candidate/Profile state,
  private application Services, and independent multi-entry Publications.
- Leased, retryable, attempt-fenced Agent tasks with append-only audit events.
- LAN, Headscale, direct-public, and Cloudflare Tunnel publication paths;
  Agent-managed Caddy uses a private Unix socket and never receives Docker access.
- Signed 3x-ui, CPA, Keeper, and Komari Agent application packages with
  images pinned by digest.

## Development

Requirements: Go 1.26.6, Node.js 24.19, and npm.

```sh
make bootstrap
make check
make security-check
```

Start a local control plane after the checks pass:

```sh
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora center serve --data-dir .vastora/center --listen 127.0.0.1:8080 --agent-connect-url http://127.0.0.1:8080
```

In another terminal, start the web development server with `cd web && npm run
dev`. On the first visit, create the administrator, then follow the required
wizard to create a real location and confirm how Agents reach Center. The
configured Agent address is reused automatically when adding nodes.

Create an encrypted control-plane backup with a password stored in a local
`0600` file, then restore only into a new empty state directory:

```sh
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora center backup --data-dir .vastora/center --output center.vastora --password-file ./backup-password
GOTOOLCHAIN=go1.26.6 go run ./cmd/vastora center restore --input center.vastora --data-dir .vastora/restored-center --password-file ./backup-password
```

The Dockerfiles build an unprivileged Center image with the compiled web UI and
a separate Agent image. They intentionally do not provide an insecure default
command: a network-reachable Center must be started with its TLS certificate
and key.

Released versions use one public Center bootstrap command:

```sh
curl -LsSf https://get.vastora.io/install.sh | sudo sh -s -- center
```

It downloads a verified release bundle and starts the guided Center and built-in
Headscale setup. Users do not clone this repository or enter a container image
digest. Each Center then generates its own short-lived, one-line Agent install
command. The command becomes live with the first published image and release
assets; see [`deploy/center`](deploy/center/README.md) for prerequisites and
local release packaging without uploading artifacts.

## Security model

- The Center never mounts a Docker socket.
- Agent enrollment uses a short-lived, one-time token; the Agent exchanges it
  for a unique credential encrypted in its own local state.
- Catalog source content is accepted only after Ed25519 signature verification.
- Registry credentials and catalog Bearer tokens are separate from catalogs and
  stored encrypted.
- Application runtime data is Agent-local and never uploaded as configuration.
- The Center deployment stack can run a fixed-version Headscale service with a
  separate data volume; an existing Headscale control plane is also supported.
- Management pages remain private by default. Public publication requires an
  explicit high-risk confirmation and application-level authentication.

Read [the architecture](docs/architecture.md), [catalog format](docs/catalog.md),
and [threat model](docs/threat-model.md) before contributing.

## License

Apache-2.0. See [LICENSE](LICENSE).
