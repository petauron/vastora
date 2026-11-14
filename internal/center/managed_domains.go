package center

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var managedDomainKey = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
var managedDomainPrefix = regexp.MustCompile(`^[a-z]{2,8}$`)

// DNS is a host resource. These fields contain no application credentials,
// protocol configuration, TLS keys, account information or traffic ledgers.
type ManagedDomainInput struct {
	SecondaryAddress      *string `json:"secondaryAddress,omitempty"`
	ApplicationID         string  `json:"applicationId"`
	Key                   string  `json:"key"`
	Prefix                string  `json:"prefix,omitempty"`
	Hostname              string  `json:"hostname,omitempty"`
	Address               string  `json:"address"`
	ExpectedRevision      int64   `json:"expectedRevision,omitempty"`
	ExpectedRecordAddress string  `json:"expectedRecordAddress,omitempty"`
	MigrateAddressFamily  bool    `json:"migrateAddressFamily,omitempty"`
}

type ManagedDomainView struct {
	SecondaryAddress         string `json:"secondaryAddress,omitempty"`
	SecondaryRecordID        string `json:"-"`
	SecondaryExpectedAddress string `json:"-"`
	ID                       string `json:"id"`
	ApplicationID            string `json:"applicationId,omitempty"`
	AgentID                  string `json:"agentId,omitempty"`
	Key                      string `json:"key"`
	Prefix                   string `json:"prefix,omitempty"`
	Hostname                 string `json:"hostname"`
	Address                  string `json:"address"`
	Type                     string `json:"type"`
	ZoneID                   string `json:"zoneId"`
	RecordID                 string `json:"recordId,omitempty"`
	SourcePublicationID      string `json:"-"`
	ExpectedRecordAddress    string `json:"-"`
	Revision                 int64  `json:"revision"`
	AppliedRevision          int64  `json:"appliedRevision"`
	Status                   string `json:"status"`
	LastError                string `json:"lastError,omitempty"`
}

// A verified execution handover, not a shared IP or display name, authorizes
// the native replacement to retain its predecessor's publication DNS.
const managedDomainPublicationOwnerSQL = `(v.application_id=? OR v.application_id IN (
 SELECT r.application_id FROM application_execution_retirements r
 JOIN applications retired ON retired.id=r.application_id
 WHERE r.replacement_application_id=? AND retired.node_id=?))`

const managedDomainColumns = `id,COALESCE(application_id,''),COALESCE(agent_id,''),resource_key,prefix,hostname,address,record_type,zone_id,record_id,source_publication_id,expected_record_address,desired_revision,applied_revision,status,last_error,secondary_address,secondary_record_id,secondary_expected_address`

func scanManagedDomain(row interface{ Scan(...any) error }) (ManagedDomainView, error) {
	var v ManagedDomainView
	err := row.Scan(&v.ID, &v.ApplicationID, &v.AgentID, &v.Key, &v.Prefix, &v.Hostname, &v.Address, &v.Type, &v.ZoneID, &v.RecordID, &v.SourcePublicationID, &v.ExpectedRecordAddress, &v.Revision, &v.AppliedRevision, &v.Status, &v.LastError, &v.SecondaryAddress, &v.SecondaryRecordID, &v.SecondaryExpectedAddress)
	return v, err
}

func (s *Store) ManagedDomain(ctx context.Context, id string) (ManagedDomainView, error) {
	return scanManagedDomain(s.db.QueryRowContext(ctx, `SELECT `+managedDomainColumns+` FROM managed_domains WHERE id=?`, id))
}

func (s *Store) ListManagedDomains(ctx context.Context, applicationID string) ([]ManagedDomainView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+managedDomainColumns+` FROM managed_domains WHERE application_id=? ORDER BY resource_key LIMIT 64`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ManagedDomainView{}
	for rows.Next() {
		v, err := scanManagedDomain(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) validateManagedDomainAddress(ctx context.Context, agentID, address string) error {
	ip := net.ParseIP(address)
	if ip == nil || !isPublicPublicationVerificationIP(ip) {
		return errors.New("center: managed DNS requires a public address")
	}
	profile, err := networkProfile(ctx, s.db, agentID)
	if err != nil {
		return err
	}
	if profile != nil && profile.ConfirmedAt.Before(s.now().Add(time.Second)) && profile.PublicAddress == address {
		return nil
	}
	candidates, err := networkCandidates(ctx, s.db, agentID)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Kind == "public" && candidate.Address == address && !candidate.ObservedAt.After(s.now()) && s.now().Sub(candidate.ObservedAt) <= agentConnectedMaxAge {
			return nil
		}
	}
	return errors.New("center: DNS address is not a confirmed or freshly observed public address of this node")
}

// EnsureManagedDomain returns the same hostname on retries. Address changes
// require an exact revision; changing a prefix never silently rotates a name.
func (s *Store) EnsureManagedDomain(ctx context.Context, in ManagedDomainInput) (ManagedDomainView, error) {
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	invalid := errors.New("center: invalid managed domain request")
	if !managedDomainKey.MatchString(in.Key) || in.Prefix != "" && !managedDomainPrefix.MatchString(in.Prefix) || in.ApplicationID == "" {
		return ManagedDomainView{}, invalid
	}
	if in.Hostname != "" && (in.Hostname != strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Hostname), ".")) || !domainSuffixPattern.MatchString(in.Hostname)) {
		return ManagedDomainView{}, invalid
	}
	in.Address = strings.TrimSpace(in.Address)
	if ip := net.ParseIP(in.Address); ip != nil {
		in.Address = ip.String()
	}
	if in.ExpectedRecordAddress != "" {
		ip := net.ParseIP(in.ExpectedRecordAddress)
		if ip == nil || !isPublicPublicationVerificationIP(ip) {
			return ManagedDomainView{}, invalid
		}
		in.ExpectedRecordAddress = ip.String()
	}
	var agentID, status, zoneName, zoneID string
	if err := s.db.QueryRowContext(ctx, `SELECT a.node_id,n.status FROM applications a JOIN agents n ON n.id=a.node_id WHERE a.id=? AND a.status='running'`, in.ApplicationID).Scan(&agentID, &status); err != nil || status != "active" {
		return ManagedDomainView{}, errors.New("center: running application on an active node required")
	}
	if err := s.validateManagedDomainAddress(ctx, agentID, in.Address); err != nil {
		return ManagedDomainView{}, err
	}
	secondary := ""
	if in.SecondaryAddress != nil {
		secondary = strings.TrimSpace(*in.SecondaryAddress)
		if secondary != "" {
			ip := net.ParseIP(secondary)
			primary := net.ParseIP(in.Address)
			if ip == nil || primary == nil || (ip.To4() == nil) == (primary.To4() == nil) {
				return ManagedDomainView{}, invalid
			}
			secondary = ip.String()
			if err := s.validateManagedDomainAddress(ctx, agentID, secondary); err != nil {
				return ManagedDomainView{}, err
			}
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint,zone_id FROM network_integrations WHERE kind='cloudflare' AND status='configured'`).Scan(&zoneName, &zoneID); err != nil {
		return ManagedDomainView{}, errors.New("center: configure managed DNS first")
	}
	current, err := scanManagedDomain(s.db.QueryRowContext(ctx, `SELECT `+managedDomainColumns+` FROM managed_domains WHERE application_id=? AND resource_key=?`, in.ApplicationID, in.Key))
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	if err == nil {
		if current.AgentID != agentID || current.ZoneID != zoneID || current.Prefix != in.Prefix || in.Hostname != "" && current.Hostname != in.Hostname || current.Status == "retiring" {
			return current, errors.New("center: managed domain ownership changed")
		}
		if in.SecondaryAddress != nil && secondary != current.SecondaryAddress {
			if in.ExpectedRevision != current.Revision || in.Hostname != current.Hostname || in.Address != current.Address || current.Status != "ready" || current.AppliedRevision != current.Revision || in.MigrateAddressFamily {
				return current, errors.New("center: dual-stack change requires the exact ready domain and revision")
			}
			result, e := s.db.ExecContext(ctx, `UPDATE managed_domains SET secondary_address=?,secondary_expected_address=secondary_address,desired_revision=desired_revision+1,status='pending',attempt=0,retry_at='',updated_at=? WHERE id=? AND desired_revision=?`, secondary, stamp, current.ID, current.Revision)
			if e != nil {
				return current, e
			}
			if count, e := result.RowsAffected(); e != nil || count != 1 {
				return current, errors.New("center: managed DNS revision changed")
			}
		}
		if current.Address != in.Address {
			newType := "A"
			if net.ParseIP(in.Address).To4() == nil {
				newType = "AAAA"
			}
			if newType != current.Type {
				if current.SecondaryAddress != "" || current.SecondaryRecordID != "" || !in.MigrateAddressFamily || in.Hostname != current.Hostname || in.ExpectedRecordAddress != current.Address || current.RecordID == "" || current.Status != "ready" || current.AppliedRevision != current.Revision {
					return current, errors.New("center: address family migration requires the exact ready domain, revision and verified previous address")
				}
			}
			if in.ExpectedRevision != current.Revision {
				return current, errors.New("center: managed domain revision changed")
			}
			_, err = s.db.ExecContext(ctx, `UPDATE managed_domains SET address=?,record_type=?,expected_record_address=?,desired_revision=desired_revision+1,status='pending',attempt=0,retry_at='',updated_at=? WHERE id=? AND desired_revision=?`, in.Address, newType, current.Address, stamp, current.ID, current.Revision)
			if err != nil {
				return current, err
			}
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_domains WHERE application_id=?`, in.ApplicationID).Scan(&count); err != nil {
			return current, err
		}
		if count >= 64 || in.ExpectedRevision != 0 || in.MigrateAddressFamily {
			return current, invalid
		}
		hostname, sourceID := in.Hostname, ""
		if hostname == "" && in.Prefix == "" {
			// Reuse this application or its verified predecessor. Short vendor/region labels
			// are deliberately not treated as old random ingress domains.
			lookupErr := s.db.QueryRowContext(ctx, `SELECT p.hostname,p.id FROM publications p JOIN services v ON v.id=p.service_id WHERE `+managedDomainPublicationOwnerSQL+` AND p.entry_node_id=? AND p.dns_provider='cloudflare' AND p.cleanup_pending=0 AND length(substr(p.hostname,1,instr(p.hostname,'.')-1))>=24 AND p.hostname LIKE ? ORDER BY p.status='ready' DESC,p.updated_at DESC LIMIT 1`, in.ApplicationID, in.ApplicationID, agentID, agentID, "%."+zoneName).Scan(&hostname, &sourceID)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return current, lookupErr
			}
		}
		if hostname == "" {
			label, e := randomDNSLabel(16)
			if e != nil {
				return current, e
			}
			if in.Prefix != "" {
				label = in.Prefix + "-" + label[:10]
			}
			hostname = label + "." + zoneName
		}
		if !strings.HasSuffix(hostname, "."+zoneName) {
			return current, errors.New("center: managed domain is outside the configured zone")
		}
		if sourceID == "" {
			lookupErr := s.db.QueryRowContext(ctx, `SELECT p.id FROM publications p JOIN services v ON v.id=p.service_id WHERE `+managedDomainPublicationOwnerSQL+` AND p.entry_node_id=? AND p.hostname=? AND p.dns_provider='cloudflare' AND p.cleanup_pending=0 ORDER BY p.updated_at DESC LIMIT 1`, in.ApplicationID, in.ApplicationID, agentID, agentID, hostname).Scan(&sourceID)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return current, lookupErr
			}
		}
		id, e := randomToken(18)
		if e != nil {
			return current, e
		}
		recordType := "A"
		if net.ParseIP(in.Address).To4() == nil {
			recordType = "AAAA"
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_domains(id,application_id,agent_id,resource_key,prefix,hostname,address,record_type,zone_id,source_publication_id,expected_record_address,status,created_at,updated_at,secondary_address) VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending',?,?,?)`, id, in.ApplicationID, agentID, in.Key, in.Prefix, hostname, in.Address, recordType, zoneID, sourceID, in.ExpectedRecordAddress, stamp, stamp, secondary)
		if err != nil {
			return current, err
		}
		current.ID = id
	} else {
		return current, err
	}
	if err = s.reconcileManagedDomainLocked(ctx, current.ID); err != nil {
		return s.managedDomainFailure(ctx, current.ID, err)
	}
	return s.ManagedDomain(ctx, current.ID)
}

type managedCloudflareRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// A pending family migration retains the exact previous remote tuple. Only an
// established owned record can match it; adoption and foreign records cannot.
func previousManagedDomainFamilyRecord(v ManagedDomainView, r managedCloudflareRecord) bool {
	oldIP := net.ParseIP(v.ExpectedRecordAddress)
	if oldIP == nil || v.RecordID == "" || r.ID != v.RecordID || r.Comment != "vastora-managed-domain:"+v.ID || r.Content != v.ExpectedRecordAddress {
		return false
	}
	oldType := "A"
	if oldIP.To4() == nil {
		oldType = "AAAA"
	}
	return oldType != v.Type && r.Type == oldType
}

func (s *Store) reconcileManagedDomainLocked(ctx context.Context, id string) error {
	v, err := s.ManagedDomain(ctx, id)
	if err != nil {
		return err
	}
	client, err := s.cloudflareWithScopes(ctx, "dns.write")
	if err != nil {
		return err
	}
	if client.zoneID != v.ZoneID {
		return errors.New("center: managed domain zone changed; restore its DNS integration")
	}
	path := "/zones/" + url.PathEscape(v.ZoneID) + "/dns_records"
	marker := "vastora-managed-domain:" + v.ID
	var records []managedCloudflareRecord
	if err = client.do(ctx, http.MethodGet, path+"?name="+url.QueryEscape(v.Hostname)+"&per_page=100", nil, &records); err != nil {
		return err
	}
	retiring := v.Status == "retiring" || v.ApplicationID == "" || v.AgentID == ""
	primaryRecords, secondaryRecord, err := s.managedDomainRecordSet(ctx, v, records)
	if err != nil {
		return err
	}
	records = primaryRecords
	if retiring {
		if len(records) == 1 {
			r := records[0]
			if v.RecordID != "" && r.ID != v.RecordID || r.Comment != marker || r.Name != v.Hostname || r.Proxied || (!(r.Type == v.Type && r.Content == v.Address) && !previousManagedDomainFamilyRecord(v, r)) {
				return errors.New("center: DNS ownership changed; record was not deleted")
			}
			if err = client.deleteDNSRecord(ctx, r.ID); err != nil {
				return err
			}
		}
		if secondaryRecord != nil {
			if err = client.deleteDNSRecord(ctx, secondaryRecord.ID); err != nil {
				return err
			}
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM managed_domains WHERE id=?`, id)
		return err
	}
	// Recover a previously refused adoption after its normal native handover
	// was recorded. Do not replace an established source or remote identity.
	if v.SourcePublicationID == "" && v.RecordID == "" {
		lookup := s.db.QueryRowContext(ctx, `SELECT p.id FROM publications p JOIN services v ON v.id=p.service_id WHERE `+managedDomainPublicationOwnerSQL+` AND p.entry_node_id=? AND p.hostname=? AND p.dns_provider='cloudflare' AND p.cleanup_pending=0 ORDER BY p.updated_at DESC LIMIT 1`, v.ApplicationID, v.ApplicationID, v.AgentID, v.AgentID, v.Hostname).Scan(&v.SourcePublicationID)
		if lookup != nil && !errors.Is(lookup, sql.ErrNoRows) {
			return lookup
		}
	}
	if err = s.validateManagedDomainAddress(ctx, v.AgentID, v.Address); err != nil {
		return err
	}
	var record managedCloudflareRecord
	if len(records) == 1 {
		record = records[0]
		if record.Name != v.Hostname || record.Proxied || record.Type != v.Type && !previousManagedDomainFamilyRecord(v, record) {
			return errors.New("center: an incompatible DNS record already exists")
		}
		var foreignReferences int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services v ON v.id=p.service_id WHERE p.dns_record_id=? AND p.hostname=? AND (NOT `+managedDomainPublicationOwnerSQL+` OR p.entry_node_id<>? OR p.cleanup_pending<>0)`, record.ID, v.Hostname, v.ApplicationID, v.ApplicationID, v.AgentID, v.AgentID).Scan(&foreignReferences); err != nil {
			return err
		}
		if foreignReferences != 0 {
			return errors.New("center: DNS record has other owners; leave their lifecycle unchanged")
		}
		if record.Comment != marker {
			var owned int
			if v.SourcePublicationID != "" {
				err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services v ON v.id=p.service_id WHERE p.id=? AND `+managedDomainPublicationOwnerSQL+` AND p.entry_node_id=? AND p.hostname=? AND p.dns_provider='cloudflare' AND p.cleanup_pending=0 AND p.dns_record_id=?`, v.SourcePublicationID, v.ApplicationID, v.ApplicationID, v.AgentID, v.AgentID, v.Hostname, record.ID).Scan(&owned)
				if err != nil {
					return err
				}
			}
			expected := v.Address
			if v.ExpectedRecordAddress != "" {
				expected = v.ExpectedRecordAddress
			}
			if owned != 1 || record.Content != expected {
				return errors.New("center: existing DNS record is not owned by this application")
			}

		} else {
			if v.RecordID != "" && record.ID != v.RecordID {
				return errors.New("center: managed DNS record identity changed")
			}
			if record.Content != v.Address && (v.ExpectedRecordAddress == "" || record.Content != v.ExpectedRecordAddress) {
				return errors.New("center: managed DNS record content changed outside this operation")
			}
		}
	}
	body := map[string]any{"name": v.Hostname, "type": v.Type, "content": v.Address, "proxied": false, "ttl": 300, "comment": marker}
	method, target := http.MethodPost, path
	if record.ID != "" {
		method, target = http.MethodPut, path+"/"+url.PathEscape(record.ID)
	}
	var applied managedCloudflareRecord
	if err = client.do(ctx, method, target, body, &applied); err != nil {
		return err
	}
	if applied.ID == "" || record.ID != "" && applied.ID != record.ID || applied.Name != v.Hostname || applied.Type != v.Type || applied.Content != v.Address || applied.Proxied || applied.Comment != marker {
		return errors.New("center: DNS provider returned an unexpected managed record")
	}
	// Persist the primary identity before the second provider operation. A lost
	// response or partial pair remains pending and reuses this exact record.
	if _, err = s.db.ExecContext(ctx, `UPDATE managed_domains SET record_id=? WHERE id=? AND desired_revision=?`, applied.ID, v.ID, v.Revision); err != nil {
		return err
	}
	if err = s.reconcileManagedSecondary(ctx, client, v, secondaryRecord, path, marker); err != nil {
		return err
	}
	// Persist external identity even when the owner was concurrently removed.
	// SET NULL leaves a cleanup tombstone rather than orphaning a remote record.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if v.SourcePublicationID != "" {
		var cleanupPending int
		if err = tx.QueryRowContext(ctx, `SELECT cleanup_pending FROM publications WHERE id=?`, v.SourcePublicationID).Scan(&cleanupPending); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cleanupPending != 0 {
			return errors.New("center: source publication cleanup raced with DNS adoption; retry after cleanup")
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE managed_domains SET record_id=?,source_publication_id=?,expected_record_address='',secondary_expected_address='',applied_revision=desired_revision,status=CASE WHEN application_id IS NULL OR agent_id IS NULL THEN 'retiring' ELSE 'ready' END,attempt=0,retry_at='',last_error='',updated_at=? WHERE id=? AND desired_revision=?`, applied.ID, v.SourcePublicationID, s.now().UTC().Format(time.RFC3339Nano), id, v.Revision); err != nil {
		return err
	}
	if v.SourcePublicationID != "" {
		// The publication retains its route. DNS lifecycle is now delegated to
		// this generic resource, so stopping the old route cannot delete its DNS.
		if _, err = tx.ExecContext(ctx, `UPDATE publications SET dns_record_id='',managed_domain_id=? WHERE hostname=? AND entry_node_id=? AND cleanup_pending=0 AND (id=? OR dns_record_id=?) AND service_id IN (SELECT v.id FROM services v WHERE `+managedDomainPublicationOwnerSQL+`)`, id, v.Hostname, v.AgentID, v.SourcePublicationID, applied.ID, v.ApplicationID, v.ApplicationID, v.AgentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) managedDomainFailure(ctx context.Context, id string, cause error) (ManagedDomainView, error) {
	// API errors may contain provider details. Store an actionable category,
	// never credentials, request headers or a raw upstream response.
	// A timed-out provider request must still receive bounded retry backoff.
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
	}
	message := "Managed DNS could not be reconciled; check integration, address and record ownership."
	_, err := s.db.ExecContext(ctx, `UPDATE managed_domains SET status=CASE WHEN status='retiring' OR application_id IS NULL OR agent_id IS NULL THEN 'retiring' ELSE 'failed' END,last_error=?,attempt=MIN(attempt+1,12),retry_at=?,updated_at=? WHERE id=?`, message, s.now().Add(15*time.Minute).UTC().Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), id)
	value, readErr := s.ManagedDomain(ctx, id)
	return value, errors.Join(cause, err, readErr)
}

func (s *Store) RetireManagedDomain(ctx context.Context, id string) error {
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	var references int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE managed_domain_id=? AND status<>'stopped'`, id).Scan(&references); err != nil {
		return err
	}
	if references != 0 {
		return errors.New("center: stop publications referencing this domain before retiring it")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_domains SET status='retiring',retry_at='',updated_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("center: managed domain not found")
	}
	if err := s.reconcileManagedDomainLocked(ctx, id); err != nil {
		_, failure := s.managedDomainFailure(ctx, id, err)
		return failure
	}
	return nil
}

// Retry pending DNS work and cleanup detached owners, never probe proxy traffic
// or withdraw a running subscription because an external endpoint is offline.
func (s *Store) RunManagedDomains(ctx context.Context, report func(error)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM managed_domains WHERE (status IN ('pending','failed','retiring') OR application_id IS NULL OR agent_id IS NULL) AND (retry_at='' OR retry_at<=?) ORDER BY updated_at LIMIT 32`, s.now().UTC().Format(time.RFC3339Nano))
		ids := []string{}
		if err == nil {
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			err = errors.Join(err, rows.Err(), rows.Close())
		}
		if err != nil && report != nil {
			report(err)
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			item, cancel := context.WithTimeout(ctx, 20*time.Second)
			s.publicationCleanupMu.Lock()
			err = s.reconcileManagedDomainLocked(item, id)
			if err != nil {
				_, _ = s.managedDomainFailure(item, id, err)
			}
			s.publicationCleanupMu.Unlock()
			cancel()
			if err != nil && report != nil {
				report(errors.New("center: managed DNS reconciliation requires attention"))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
