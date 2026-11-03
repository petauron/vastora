# Promote an existing landing-only node

Issue: #1183. This explicit, one-way maintenance operation retains the enrolled
Agent, credential, private-network identity, installed applications and history.
It enables the existing Docker-backed worker/gateway architecture. It does not
install a public proxy, enroll a replacement node or authorize application changes.

1. Upgrade the Center and this Agent to a release supporting this operation.
   Scope automatic updates before changing the Center if other nodes must remain
   untouched. Resolve existing node tasks first. Verify SSH recovery, free disk
   space and a current consistent backup of the Agent database, original key and
   systemd unit. Reuse a verified applicable recovery point rather than making
   repeated copies. Preserve the Center database/key before schema migration.
2. With explicit authority for this node, install Docker using the existing
   Center-provided Docker prerequisite installer. Confirm its daemon is ready.
   This step does not change the landing-only authorization.
3. On the Center host, using the existing Center state and original key, run:

   ```sh
   vastora center authorize-gateway-promotion \
     --data-dir /var/lib/vastora/center --agent-id EXISTING_AGENT_ID --confirm
   ```

   Keep the returned promotion ID. Authorization binds to this exact machine's
   existing credential and expires after 15 minutes if it has not been prepared.
4. On that node, stop only its Vastora Agent, then apply the exact grant:

   ```sh
   systemctl stop vastora-agent.service
   vastora agent configure --data-dir /var/lib/vastora/agent \
     --roles worker,gateway --capabilities docker,gateway \
     --promotion-id PROMOTION_ID
   ```

   The original pinned Center authenticates preparation before the local purpose
   changes. Prepared nodes cannot claim tasks. The new startup heartbeat must
   report exactly the approved worker/gateway purpose before the Center confirms
   the transition and resumes task delivery. Local flags and ordinary heartbeats
   still cannot override landing-only enrollment.
5. Confirm the same Agent is connected with its new purpose. Check private
   networking and the retained applications, monitoring identity and data. Any
   subsequent gateway/proxy/subscription work uses its existing management flow
   and must be separately within the approved scope. Process status alone is not
   end-to-end acceptance.

If preparation or local configuration fails, retain the same promotion ID and
recovery point. Inspect the error and local service/unit before restarting; do
not re-enroll or edit database flags. A prepared grant remains retryable because
the local change may already have completed even when its response was lost.
Stop the Agent before retrying the same configure command. It is idempotent for
that grant and original identity. There is no automatic purpose downgrade or
database rollback; use forward recovery if an operator intervention is needed.
