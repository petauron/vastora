# v0.1 roadmap

Vastora is intentionally released as complete vertical slices, not as a
collection of empty screens or speculative interfaces. The current foundation
implements versioned Catalog schemas, portable signing/verification fixtures,
signed refresh, encrypted Center and Agent local state,
administrator setup, encrypted Center backup and recovery, multi-Site topology,
typed private Service and multi-entry Publication orchestration, Agent-managed
Caddy/cloudflared components, Headscale integration, and the bilingual console.
The typed Agent executor validates signed manifests before Docker access, uses
one-request Registry authentication for matching digest-pinned images, and can
restart its last-known-good application state with locally encrypted secrets
from verified local artifacts while Center is unavailable.

The following slices remain before `v0.1.0`:

1. Publish and enforce the complete Center/Agent v1 OpenAPI contract.
2. Expand the typed application executor beyond the initial official package allowlist.
3. Multi-user RBAC and task revocation.
4. Production TLS deployment guidance and additional DNS provider integrations.
5. Release packaging, upgrade recovery, and destructive-failure testing.
6. Metrics and logs capability handlers; the protocol fields are currently
   reserved and are not advertised.
7. Multi-user Headscale policy administration beyond the built-in Vastora tags.
8. Real two-node Worker/Gateway and public-node acceptance testing. Current
   development acceptance is intentionally single-node; direct public ingress
   remains covered by simulated integration tests until a public node exists.

An app's only deployable identity is `source-id/app-id` plus its immutable
semantic `version`; an image digest is part of that version's content. There
are no marketplace revisions, old-version selector, automatic rollback, leader
election, or silent cross-source overrides in v0.1.
