package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func (s *Store) networkCandidates(ctx context.Context, agentID string) ([]networking.Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT address, interface_name, family, kind, observed_at FROM agent_network_candidates WHERE agent_id = ? ORDER BY kind, interface_name, address`, agentID)
	if err != nil {
		return nil, fmt.Errorf("center: list network candidates: %w", err)
	}
	defer rows.Close()
	values := []networking.Candidate{}
	for rows.Next() {
		var value networking.Candidate
		var observed string
		if err := rows.Scan(&value.Address, &value.Interface, &value.Family, &value.Kind, &observed); err != nil {
			return nil, err
		}
		value.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			return nil, errors.New("center: invalid network candidate timestamp")
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) networkProfile(ctx context.Context, agentID string) (*networking.Profile, error) {
	var value networking.Profile
	var enabled []byte
	var direct int
	var confirmed, observed string
	err := s.db.QueryRowContext(ctx, `SELECT service_address, lan_address, headscale_address, public_address, enabled_kinds_json, direct_public, confirmed_at, candidate_observed_at FROM agent_network_profiles WHERE agent_id = ?`, agentID).Scan(&value.ServiceAddress, &value.LANAddress, &value.HeadscaleAddress, &value.PublicAddress, &enabled, &direct, &confirmed, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read network profile: %w", err)
	}
	if json.Unmarshal(enabled, &value.EnabledKinds) != nil {
		return nil, errors.New("center: invalid stored network profile")
	}
	value.DirectPublic = direct == 1
	value.ConfirmedAt, err = time.Parse(time.RFC3339Nano, confirmed)
	if err != nil {
		return nil, errors.New("center: invalid network profile timestamp")
	}
	value.CandidateObserved, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		return nil, errors.New("center: invalid network observation timestamp")
	}
	return &value, nil
}

func (s *Store) ConfirmNetworkProfile(ctx context.Context, agentID string, input networking.Profile) (*networking.Profile, error) {
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active'`, agentID).Scan(&active); err != nil {
		return nil, fmt.Errorf("center: inspect Agent: %w", err)
	}
	if active == 0 {
		return nil, errors.New("center: active Agent not found")
	}
	input.ServiceAddress = strings.TrimSpace(input.ServiceAddress)
	input.LANAddress = strings.TrimSpace(input.LANAddress)
	input.HeadscaleAddress = strings.TrimSpace(input.HeadscaleAddress)
	input.PublicAddress = strings.TrimSpace(input.PublicAddress)
	input.EnabledKinds = uniqueStrings(input.EnabledKinds)
	sort.Strings(input.EnabledKinds)
	candidates, err := s.networkCandidates(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("center: Agent has not reported any network addresses")
	}
	if err := networking.ValidateProfile(candidates, input); err != nil {
		return nil, err
	}
	previous, err := s.networkProfile(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.ServiceAddress != input.ServiceAddress {
		var applicationCount int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE node_id = ? AND status NOT IN ('stopped', 'failed')`, agentID).Scan(&applicationCount); err != nil {
			return nil, err
		}
		if applicationCount != 0 {
			return nil, errors.New("center: stop applications before changing the private service address")
		}
	}
	latest := candidates[0].ObservedAt
	for _, candidate := range candidates[1:] {
		if candidate.ObservedAt.After(latest) {
			latest = candidate.ObservedAt
		}
	}
	input.CandidateObserved = latest
	input.ConfirmedAt = s.now().UTC()
	if !input.DirectPublic {
		var publicationCount int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id WHERE (a.node_id = ? OR p.gateway_node_id = ?) AND p.kind IN ('public_direct', 'public_shared_443') AND p.status <> 'stopped'`, agentID, agentID).Scan(&publicationCount); err != nil {
			return nil, err
		}
		if publicationCount != 0 {
			return nil, errors.New("center: stop direct public publications before disabling public ingress")
		}
	}
	enabled := make(map[string]bool, len(input.EnabledKinds))
	for _, kind := range input.EnabledKinds {
		enabled[kind] = true
	}
	checks := []struct{ kind, publication string }{{networking.KindLAN, publicationLAN}, {networking.KindHeadscale, publicationHeadscale}}
	for _, check := range checks {
		if enabled[check.kind] {
			continue
		}
		var publicationCount int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND kind = ? AND status <> 'stopped'`, agentID, check.publication).Scan(&publicationCount); err != nil {
			return nil, err
		}
		if publicationCount != 0 {
			return nil, fmt.Errorf("center: stop %s publications before disabling this network", check.kind)
		}
	}
	enabledJSON, _ := json.Marshal(input.EnabledKinds)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_profiles(agent_id, service_address, lan_address, headscale_address, public_address, enabled_kinds_json, direct_public, confirmed_at, candidate_observed_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET service_address = excluded.service_address, lan_address = excluded.lan_address, headscale_address = excluded.headscale_address, public_address = excluded.public_address, enabled_kinds_json = excluded.enabled_kinds_json, direct_public = excluded.direct_public, confirmed_at = excluded.confirmed_at, candidate_observed_at = excluded.candidate_observed_at`, agentID, input.ServiceAddress, input.LANAddress, input.HeadscaleAddress, input.PublicAddress, enabledJSON, input.DirectPublic, input.ConfirmedAt.Format(time.RFC3339Nano), input.CandidateObserved.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("center: save network profile: %w", err)
	}
	if err := s.autoAssignFirstSiteGateway(ctx, tx, agentID, s.now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.networkProfile(ctx, agentID)
}

func (s *Store) autoAssignFirstSiteGateway(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) error {
	var siteID string
	var capabilitiesJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT site_id, capabilities_json FROM agents WHERE id = ?`, agentID).Scan(&siteID, &capabilitiesJSON); err != nil {
		return fmt.Errorf("center: inspect Agent gateway capability: %w", err)
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil {
		return errors.New("center: invalid stored Agent capabilities")
	}
	if !capabilities.Gateway {
		return nil
	}
	var gatewayCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_gateways WHERE site_id = ?`, siteID).Scan(&gatewayCount); err != nil {
		return fmt.Errorf("center: inspect Site gateways: %w", err)
	}
	if gatewayCount != 0 {
		return nil
	}
	return s.replaceSiteGateways(ctx, tx, siteID, []string{agentID}, now)
}
