# Petauron Meridian app package

Meridian is Vastora's native managed access and subscription data plane. Each
entry installation runs only the pinned official Xray image; there is no node
panel, node database, controller role, or node-side subscription service. The
single installation colocated with Center may own only the public subscription
service and therefore has no local Xray runtime. Package presence alone never
claims that a proxy endpoint exists.

Vastora Center is the single account and subscription authority. It creates a
complete desired revision through the Meridian domain module and sends that
secret-bearing artifact to the owning Agent. The Agent validates the candidate
with the same pinned Xray image, promotes it atomically, and returns a receipt
bound to both the revision and configuration SHA-256. A stale or different
runtime cannot satisfy that receipt.

HAProxy remains the sole public TCP/443 listener. It forwards Proxy Protocol v2
over the shared Docker bridge to Xray's internal TCP/443 listener. Fixed landing
credentials route TCP to the selected private SOCKS peer and explicitly reject
UDP or missing peers; they never fall back to the entry node's direct exit.
When one landing runtime is unavailable, Center marks only that route blocked
and projects the remaining credentials. Last-applied subscription recovery
also removes the unavailable route and preserves `hide native` semantics.
Hysteria2 remains a native UDP/443 entry. Vastora does not issue or publish a
routed Hysteria credential because current Xray Hysteria inbounds do not offer
a reliable per-user routing boundary.

Xray user counters are observed as monotonic per-credential watermarks. Center
projects all native and routed credentials for one account into a single shared
quota and changes runtime enablement only when the quota or expiry boundary is
crossed. Ordinary observations do not reload Xray.

The forward cutover imports existing token, UUID, REALITY, traffic, and public
subscription identities once. Center first publishes the imported snapshot at
the existing subscription URL and waits for its gateway or tunnel receipt;
until then the old origin remains live. Only after that receipt does the first
verified projection atomically replace each old proxy process while retaining
migration-only Docker aliases. The owned volumes, journal, API adapter
authority, temporary aliases, and old catalog identity are retired only after
every runtime and public entry is verified; there is no dual-write or runtime
fallback path.
The transition release retains legacy tables and the export reader only as
unreachable forward-migration input. A later tested forward-only cleanup
migration removes that source after fleet-wide cutover is confirmed.

Preflight fails closed if the legacy inventory contains a per-entry traffic
ceiling or per-client simultaneous-IP limit. Meridian deliberately models one
shared quota per account, and current Xray has no equivalent built-in IP-limit
policy; the cutover never weakens either restriction silently.
