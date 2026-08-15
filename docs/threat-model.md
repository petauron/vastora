# Threat model

## Trust boundaries

- A Master administrator can configure applications and therefore is trusted
  with their startup secrets.
- A Node administrator and the local Docker daemon can inspect a running
  container's environment and mounted secret files. Vastora cannot remove this
  host-root trust boundary.
- Catalog transport can be private or public; catalog integrity comes from the
  pinned Ed25519 public key, not transport alone.

## Controls

- Master secrets, catalog Bearer tokens, and registry credentials use
  AES-256-GCM with a root key stored outside SQLite.
- Catalog documents are bounded in size, decoded only after signature
  verification, and cached only after validation succeeds.
- The Master never mounts Docker's socket. A future Node deployment executor
  will allow only schema-declared Compose inputs and no arbitrary shell hooks.
- The setup token is stored as a SHA-256 hash and becomes unusable after the
  first administrator is created.

## Non-goals for v0.1

Vastora does not protect against a malicious root user on a Node, operate a
quorum or leader election system, provide multi-user RBAC, or collect telemetry
by default.
