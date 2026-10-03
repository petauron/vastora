# Node native egress policy

Node settings → Egress IP policy supports `auto`, `ipv4_only`, and `ipv6_only` for one running Meridian native endpoint. The setting governs the final native direct outbound, independently of the subscription entry address family. Existing entry addresses, REALITY identity and fixed remote landing routes are retained. Remote landing traffic is outside this setting; no claim is made about its final family.

Strict modes require the selected family to be usable in the actual host runtime network namespace. They reject unsupported targets, opposite-family IP literals and UDP destinations; they do not retry using the other family. Private/reserved destination restrictions remain. Preferred-family connection fallback modes are deferred and rejected by the API.

## Applying

An online Agent advertising `nativeEgress`, an intact running host-network Meridian runtime, and an enabled native credential are required. An explicit first policy application selects the digest-pinned Xray 26.9.30 core needed for strict UDP enforcement. This may interrupt existing connections. Returning to automatic mode retains that core. Nodes with no policy record keep their current runtime and automatic behavior.

The existing durable task channel owns the change. The Agent checks actual runtime network access before a strict application, applies the complete managed artifact, and makes a TLS-verified HTTPS exit request through an isolated real client for each enabled native protocol (VLESS and HY2). Client probes use the original protocol identity and the node's private runtime address, not a substitute direct HTTP connection. Temporary client configuration travels over the encrypted channel and stdin and is cleaned up before success.

The UI distinguishes desired policy, apply failures and the last verified policy/exit/time. A successful core reload alone is insufficient. Missing, stale, wrong-family or wrong-artifact evidence cannot mark the policy verified. Failed/uncertain tasks use the existing explicit recovery flow in Activity; neither a fallback nor an automatic retry is performed. A verified policy persists through subsequent credential/quota projections without requiring an enabled credential to revoke access.

## Storage and acceptance boundaries

Schema 106 is forward-only and uses the existing pre-migration backup and fail-closed migration mechanism. Empty policy storage represents the existing automatic behavior, without restarting any node. API mutations require the existing admin session and CSRF protection plus optimistic revision and execution fences.

Automated CI covers the migration, failed evidence, identity preservation, unsupported policies and continued quota enforcement. The Meridian renderer's strict rules have separate TCP/UDP runtime evidence. A successful native HTTPS probe reports the observed family at that time; it does not promise that every destination, UDP service, provider network or later connection is reachable. Production rollout and a live policy switch are separate from source delivery.
