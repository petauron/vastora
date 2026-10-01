package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// Deployment input alone does not contain Meridian's retained desired state.
// Bind it to the review without returning credentials or requiring that a
// damaged configuration can already compile. Observations and timestamps do
// not change the review. Keep this separate from package authorization: runtime
// restoration reserves a new endpoint revision after the image is prepared.
func reinstallMeridianEvidence(ctx context.Context, tx *sql.Tx, agentID string) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for _, query := range []string{
		`SELECT json_array(p.id,p.service_id,p.kind,p.ingress_owner,p.entry_node_id,p.hostname,p.sni_hostname,
 p.dns_provider,p.desired_revision,p.status='stopped',p.action_required,p.cleanup_pending,s.application_id,s.status='stopped',
 s.app_protocol,s.endpoint,s.container_port,a.runtime,a.role,a.runtime_generation)
 FROM publications p JOIN services s ON s.id=p.service_id JOIN applications a ON a.id=s.application_id
 WHERE a.node_id=? ORDER BY p.id`,
		`SELECT json_array(e.id,e.application_id,e.service_id,e.inbound_tag,e.listen_address,e.listen_port,
 e.advertise_host,e.advertise_port,e.target,e.target_ip,CAST(e.server_names_json AS TEXT),e.private_key_secret_id,
 hex(private.sealed),e.public_key,CAST(e.short_ids_json AS TEXT),e.fingerprint,e.vless_enabled,e.hy2_enabled,
 e.hy2_inbound_tag,e.hy2_server_name,e.hy2_certificate_secret_id,hex(cert.sealed),e.hy2_private_key_secret_id,
 hex(hy2key.sealed),e.hy2_certificate_not_after,e.total_bytes,e.reset_day,e.desired_revision,e.status='retired',
 CAST(e.source_peer_json AS TEXT))
 FROM meridian_endpoints e JOIN applications app ON app.id=e.application_id
 LEFT JOIN secrets private ON private.id=e.private_key_secret_id
 LEFT JOIN secrets cert ON cert.id=e.hy2_certificate_secret_id
 LEFT JOIN secrets hy2key ON hy2key.id=e.hy2_private_key_secret_id
 WHERE app.node_id=? ORDER BY e.id`,
		`SELECT json_array(c.id,c.account_id,c.endpoint_id,c.kind,c.user_name,c.identity_sha256,c.protocol_secret_id,
 hex(protocol.sealed),c.hy2_auth_secret_id,hex(hy2.sealed),c.hy2_identity_sha256,c.egress_node_id,c.enabled,
 a.total_bytes,a.expiry_time,a.reset_days,a.enabled,a.desired_revision,a.status,
 a.subscription_token_secret_id,a.subscription_token_sha256,hex(token.sealed))
 FROM meridian_credentials c JOIN meridian_endpoints e ON e.id=c.endpoint_id
 JOIN applications app ON app.id=e.application_id JOIN meridian_accounts a ON a.id=c.account_id
 LEFT JOIN secrets protocol ON protocol.id=c.protocol_secret_id
 LEFT JOIN secrets hy2 ON hy2.id=c.hy2_auth_secret_id
 LEFT JOIN secrets token ON token.id=a.subscription_token_secret_id
 WHERE app.node_id=? ORDER BY c.id`,
	} {
		rows, err := tx.QueryContext(ctx, query, agentID)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var evidence string
			if err := rows.Scan(&evidence); err != nil {
				rows.Close()
				return "", err
			}
			if err := encoder.Encode(evidence); err != nil {
				rows.Close()
				return "", err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
