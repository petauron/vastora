package center

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// Native registration imports an already running, authenticated Glivela entry.
// It neither creates a legacy runtime nor transfers another entry's ledger.
type GlivelaNativeRegistrationInput struct {
	ApplicationID string `json:"applicationId"`
	Name          string `json:"name"`
	RegionCode    string `json:"regionCode"`
}

func (s *Store) RegisterGlivelaNativeEntry(ctx context.Context, adminID string, input GlivelaNativeRegistrationInput) (string, error) {
	region, _, name, err := composeRealityDisplayName(input.RegionCode, input.Name)
	if err != nil || !glivela.ValidIdentifier(input.ApplicationID) {
		return "", errors.New("center: valid native application and display name required")
	}
	now := s.now().UTC()
	path := s.glivelaPilotStatePath
	if path == "" {
		path = "/run/glivela-pilot/nodes.json"
	}
	evidence, err := meridianruntime.ReadNativeEntryRegistration(path, input.ApplicationID, now)
	if err != nil {
		return "", err
	}
	endpoint := evidence.Plan.RealityEndpoints[0]
	if !domainSuffixPattern.MatchString(endpoint.AdvertiseHost) {
		return "", errors.New("center: native publication requires a domain")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return "", err
	}
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return "", err
	}
	if !admin {
		return "", errors.New("center: administrator required")
	}
	var siteID, nodeID, created, seen, public, bind string
	err = tx.QueryRowContext(ctx, `SELECT a.site_id,a.node_id,a.created_at,agent.last_seen_at,p.public_address,p.public_bind_address
 FROM applications a JOIN agents agent ON agent.id=a.node_id JOIN agent_network_profiles p ON p.agent_id=a.node_id
 WHERE a.id=? AND a.app_key='vastora-official/glivela-entry' AND a.runtime='host' AND a.status='running' AND agent.status='active'`, input.ApplicationID).Scan(&siteID, &nodeID, &created, &seen, &public, &bind)
	if err != nil {
		return "", errors.New("center: running native Glivela application required")
	}
	createdAt, e1 := time.Parse(time.RFC3339Nano, created)
	seenAt, e2 := time.Parse(time.RFC3339Nano, seen)
	if e1 != nil || e2 != nil || evidence.Receipt.StartedAt.Before(createdAt) || seenAt.After(now) || now.Sub(seenAt) > agentConnectedMaxAge || public != endpoint.ListenAddress || bind != public {
		return "", errors.New("center: native runtime and confirmed public address do not match")
	}
	if _, _, err := validateNodeDirectPublicIngress(ctx, tx, nodeID); err != nil {
		return "", err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints WHERE application_id=? OR id=?)`, input.ApplicationID, endpoint.ID).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", errors.New("center: entry already registered; registration cannot replace configuration or counters")
	}
	if err := ensureRealityDisplayNameUnreserved(ctx, tx, siteID, name); err != nil {
		return "", err
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE app_protocol=? AND status<>'stopped' AND display_name=? COLLATE NOCASE`, meridianEntryProtocol, name).Scan(&duplicate); err != nil {
		return "", err
	}
	if duplicate != 0 {
		return "", errors.New("center: duplicate entry display name")
	}
	// This minimal direct-entry slice must not silently discard account limits.
	for _, m := range evidence.Plan.Credentials {
		var allowed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_accounts WHERE id=? AND enabled=1 AND status='active' AND total_bytes=0 AND expiry_time=0 AND reset_days=0)`, m.Credential.AccountID).Scan(&allowed); err != nil {
			return "", err
		}
		if !allowed {
			return "", errors.New("center: native direct registration requires an existing active unlimited account")
		}
	}
	provider, err := meridianEndpointDNSProvider(ctx, tx, endpoint.AdvertiseHost)
	if err != nil {
		return "", err
	}
	stamp := now.Format(time.RFC3339Nano)
	serviceID, err := randomToken(18)
	if err != nil {
		return "", err
	}
	publicationID, err := randomToken(18)
	if err != nil {
		return "", err
	}
	if err := s.ensureAllMeridianSubscriptionSnapshotsInTx(ctx, tx); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
 VALUES(?,?,?,?,?,?,'tcp',?,?,?,'observed',?,0,?,'ready',?,?)`, serviceID, input.ApplicationID, siteID, "native-entry", name, region, endpoint.ListenPort, endpoint.ListenPort, net.JoinHostPort(public, strconv.Itoa(endpoint.ListenPort)), meridianEntryProtocol, public, stamp, stamp)
	if err != nil {
		return "", err
	}
	secretID, err := s.putSecret(ctx, tx, []byte(endpoint.PrivateKey), meridianEndpointSecretContext(endpoint.ID))
	if err != nil {
		return "", err
	}
	names, _ := json.Marshal(endpoint.ServerNames)
	shorts, _ := json.Marshal(endpoint.ShortIDs)
	_, err = tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_address,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,vless_enabled,hy2_enabled,status,desired_revision,applied_revision,runtime_healthy,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,'',?,?,?,?,?,1,0,'ready',?,?,1,?,?)`, endpoint.ID, input.ApplicationID, serviceID, endpoint.InboundTag, public, endpoint.ListenPort, endpoint.AdvertiseHost, endpoint.AdvertisePort, endpoint.Target, names, secretID, endpoint.PublicKey, shorts, endpoint.Fingerprint, evidence.Plan.Revision, evidence.Plan.Revision, stamp, stamp)
	if err != nil {
		return "", err
	}
	checkpoint := meridianruntime.PilotCounters{EntryID: input.ApplicationID, CertificateID: evidence.CertificateID, ConfigSHA256: evidence.ConfigSHA256, Revision: evidence.Plan.Revision, CutoverAt: createdAt}
	for _, m := range evidence.Plan.Credentials {
		c := m.Credential
		secret, err := s.putSecret(ctx, tx, []byte(m.ProtocolID), meridianCredentialSecretContext(c.ID))
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,1,?,?)`, c.ID, c.AccountID, endpoint.ID, c.Kind, c.User, c.Identity, secret, stamp, stamp)
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) VALUES(?,0,0,0,0,?)`, c.ID, stamp)
		if err != nil {
			return "", err
		}
		checkpoint.Totals = append(checkpoint.Totals, glivela.CounterSnapshot{CredentialID: c.ID})
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES(?,?,?)`, endpoint.ID, raw, stamp)
	if err != nil {
		return "", err
	}
	if err := s.recordGlivelaPilotReceiptInTx(ctx, tx, evidence.Receipt); err != nil {
		return "", err
	}
	if err := s.recordGlivelaPilotHealthInTx(ctx, tx, endpoint.ID, checkpoint, evidence.Receipt); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,status,created_at,updated_at) VALUES(?,?,'public_direct','application_node',?,?,?,?,0,'pending',?,?)`, publicationID, serviceID, nodeID, endpoint.AdvertiseHost, endpoint.ServerNames[0], provider, stamp, stamp)
	if err != nil {
		return "", err
	}
	audit, _ := json.Marshal(map[string]string{"adminId": adminID, "applicationId": input.ApplicationID, "source": "authenticated-native-receipt"})
	if err := s.recordTaskEvent(ctx, tx, endpoint.ID, nodeID, "glivela.native.register", int64(evidence.Plan.Revision), "succeeded", string(audit)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	// Registration remains durable if DNS reconciliation needs a normal retry.
	if provider == "cloudflare" {
		if _, err := s.reconcilePublicationDNS(ctx, publicationID, nodeID, provider, 1); err != nil {
			return endpoint.ID, err
		}
	}
	return endpoint.ID, nil
}
