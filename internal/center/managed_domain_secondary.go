package center

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
)

func secondaryDomainType(primary string) string {
	if primary == "A" {
		return "AAAA"
	}
	return "A"
}

// Validate the complete remote set before touching either family. The second
// record is never adopted from another publication or inferred by matching IP.
func (s *Store) managedDomainRecordSet(ctx context.Context, v ManagedDomainView, records []managedCloudflareRecord) ([]managedCloudflareRecord, *managedCloudflareRecord, error) {
	primary := []managedCloudflareRecord{}
	var secondary *managedCloudflareRecord
	for _, r := range records {
		if r.Type == v.Type || previousManagedDomainFamilyRecord(v, r) {
			primary = append(primary, r)
			continue
		}
		if secondary != nil || (v.SecondaryAddress == "" && v.SecondaryRecordID == "") || r.Type != secondaryDomainType(v.Type) || r.Name != v.Hostname || r.Proxied || r.Comment != "vastora-managed-domain:"+v.ID || v.SecondaryRecordID != "" && r.ID != v.SecondaryRecordID || r.Content != v.SecondaryAddress && (v.SecondaryExpectedAddress == "" || r.Content != v.SecondaryExpectedAddress) {
			return nil, nil, errors.New("center: conflicting or foreign secondary DNS record")
		}
		var refs int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE dns_record_id=?`, r.ID).Scan(&refs); err != nil {
			return nil, nil, err
		}
		if refs != 0 {
			return nil, nil, errors.New("center: secondary DNS record belongs to another publication")
		}
		copy := r
		secondary = &copy
	}
	if len(primary) > 1 {
		return nil, nil, errors.New("center: conflicting records prevent managed DNS reconciliation")
	}
	return primary, secondary, nil
}
func (s *Store) reconcileManagedSecondary(ctx context.Context, client cloudflareClient, v ManagedDomainView, record *managedCloudflareRecord, path, marker string) error {
	if v.SecondaryAddress == "" {
		if record != nil {
			if err := client.deleteDNSRecord(ctx, record.ID); err != nil {
				return err
			}
		}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_domains SET secondary_record_id='' WHERE id=? AND desired_revision=?`, v.ID, v.Revision)
		return err
	}
	ip := net.ParseIP(v.SecondaryAddress)
	if ip == nil || (ip.To4() != nil) != (secondaryDomainType(v.Type) == "A") {
		return errors.New("center: invalid secondary DNS address family")
	}
	if err := s.validateManagedDomainAddress(ctx, v.AgentID, v.SecondaryAddress); err != nil {
		return err
	}
	body := map[string]any{"name": v.Hostname, "type": secondaryDomainType(v.Type), "content": v.SecondaryAddress, "proxied": false, "ttl": 300, "comment": marker}
	method, target := http.MethodPost, path
	if record != nil {
		method, target = http.MethodPut, path+"/"+url.PathEscape(record.ID)
	}
	var applied managedCloudflareRecord
	if err := client.do(ctx, method, target, body, &applied); err != nil {
		return err
	}
	if applied.ID == "" || record != nil && applied.ID != record.ID || applied.Name != v.Hostname || applied.Type != secondaryDomainType(v.Type) || applied.Content != v.SecondaryAddress || applied.Proxied || applied.Comment != marker {
		return errors.New("center: DNS provider returned unexpected secondary identity")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE managed_domains SET secondary_record_id=? WHERE id=? AND desired_revision=?`, applied.ID, v.ID, v.Revision)
	return err
}
