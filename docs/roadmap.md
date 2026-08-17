# v0.1 roadmap

Vastora is intentionally released as complete vertical slices, not as a
collection of empty screens or speculative interfaces. The current foundation
implements catalog signing and refresh, encrypted Center and Agent local state,
administrator setup, encrypted Center backup and recovery, multi-Site topology,
typed private Service and multi-entry Publication orchestration, Agent-managed
Caddy/cloudflared components, Headscale integration, and the bilingual console.

The following slices remain before `v0.1.0`:

1. Complete API and catalog interoperability fixtures.
2. Private Registry credential consumption during image pulls.
3. Expand the typed application executor beyond the initial official package allowlist.
4. Multi-user RBAC and task revocation.
5. Production TLS deployment guidance and additional DNS provider integrations.
6. Release packaging, upgrade recovery, and destructive-failure testing.
7. Metrics and logs capability handlers; the protocol fields are currently
   reserved and are not advertised.
8. Multi-user Headscale policy administration beyond the built-in Vastora tags.
9. Real two-node Worker/Gateway and public-node acceptance testing. Current
   development acceptance is intentionally single-node; direct public ingress
   remains covered by simulated integration tests until a public node exists.

An app's only deployable identity is `source-id/app-id` plus its immutable
semantic `version`; an image digest is part of that version's content. There
are no marketplace revisions, old-version selector, automatic rollback, leader
election, or silent cross-source overrides in v0.1.
