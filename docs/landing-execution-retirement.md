# Retire retained Dante execution

`vastora agent retire-landing-execution --data-dir /var/lib/vastora/agent
--native-state <owned-native-state>` is a Linux root maintenance operation.
It is not uninstall, history deletion or a subscription/accounting migration.

Keep Agent persistently stopped/masked. Glivela's node and Xray services must be
running, owned by the installed native landing application. Glivela must first
complete its authenticated full-route acceptance and retain its accepted cutover
journal plus the unchanged original checkpoint. The old Dante and firewall units
must be stopped, with the old proxy disabled at boot.

The command verifies the node identity, native resource ownership, old journal
and recovery checkpoint. Before atomically setting `executionRetired` it saves
private copies of the original journal and acceptance/checkpoint evidence with
SHA256 hashes. Encrypted Agent data, all host files and backups are retained.

After success, unmask/start Agent and verify heartbeat, Glivela reports, original
subscription identities, actual double-hop paths and accounting continuity. The
Agent still manages ordinary package lifecycle; only old landing recovery,
configuration/remove operations and health observation are fenced. A repeat is
a no-op. Never clear the marker to bypass a failed migration or rewrite SQLite.
