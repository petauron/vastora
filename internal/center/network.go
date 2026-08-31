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

type networkQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func networkCandidates(ctx context.Context, queryer networkQueryer, agentID string) ([]networking.Candidate, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT address, interface_name, kind, observed_at FROM agent_network_candidates WHERE agent_id = ? ORDER BY kind, interface_name, address`, agentID)
	if err != nil {
		return nil, fmt.Errorf("center: list network candidates: %w", err)
	}
	defer rows.Close()
	values := []networking.Candidate{}
	for rows.Next() {
		var value networking.Candidate
		var observed string
		if err := rows.Scan(&value.Address, &value.Interface, &value.Kind, &observed); err != nil {
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

func networkProfile(ctx context.Context, queryer networkQueryer, agentID string) (*networking.Profile, error) {
	var value networking.Profile
	var enabled []byte
	var direct int
	var verified, confirmed, observed string
	err := queryer.QueryRowContext(ctx, `SELECT service_address, lan_address, headscale_address, public_address, public_bind_address, public_mode, enabled_kinds_json, direct_public, public_verified_at, confirmed_at, candidate_observed_at FROM agent_network_profiles WHERE agent_id = ?`, agentID).Scan(&value.ServiceAddress, &value.LANAddress, &value.HeadscaleAddress, &value.PublicAddress, &value.PublicBindAddress, &value.PublicMode, &enabled, &direct, &verified, &confirmed, &observed)
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
	if verified != "" {
		value.PublicVerifiedAt, err = time.Parse(time.RFC3339Nano, verified)
		if err != nil {
			return nil, errors.New("center: invalid public ingress verification timestamp")
		}
	}
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

func agentPublicEgress(ctx context.Context, queryer networkQueryer, agentID string) (*networking.PublicEgress, error) {
	var value networking.PublicEgress
	var observed string
	err := queryer.QueryRowContext(ctx, `SELECT public_egress_address, public_egress_bind_address, public_egress_mode, public_egress_observed_at FROM agents WHERE id = ?`, agentID).Scan(&value.Address, &value.BindAddress, &value.Mode, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read Agent public egress: %w", err)
	}
	if value.Address == "" && value.BindAddress == "" && value.Mode == "" && observed == "" {
		return nil, nil
	}
	value.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
	if err != nil {
		return nil, errors.New("center: invalid stored Agent public egress timestamp")
	}
	return &value, nil
}

func (s *Store) ConfirmNetworkProfile(ctx context.Context, agentID string, input networking.Profile) (*networking.Profile, error) {
	input.ServiceAddress = strings.TrimSpace(input.ServiceAddress)
	input.LANAddress = strings.TrimSpace(input.LANAddress)
	input.HeadscaleAddress = strings.TrimSpace(input.HeadscaleAddress)
	input.PublicAddress = strings.TrimSpace(input.PublicAddress)
	input.PublicBindAddress = strings.TrimSpace(input.PublicBindAddress)
	input.PublicMode = strings.TrimSpace(input.PublicMode)
	input.PublicVerifiedAt = time.Time{}
	input.EnabledKinds = uniqueStrings(input.EnabledKinds)
	sort.Strings(input.EnabledKinds)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var lastSeenAtValue string
	if err := tx.QueryRowContext(ctx, `SELECT last_seen_at FROM agents WHERE id = ? AND status = 'active'`, agentID).Scan(&lastSeenAtValue); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: active Agent not found")
	} else if err != nil {
		return nil, fmt.Errorf("center: inspect Agent: %w", err)
	}
	lastSeenAt, err := time.Parse(time.RFC3339Nano, lastSeenAtValue)
	if err != nil {
		return nil, errors.New("center: invalid Agent heartbeat timestamp")
	}
	candidates, err := networkCandidates(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("center: Agent has not reported any network addresses")
	}
	previous, err := networkProfile(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.PublicAddress != input.PublicAddress {
		var publicationCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id
			WHERE (a.node_id = ? OR p.gateway_node_id = ?) AND p.kind IN ('public_direct', 'public_shared_443') AND p.status <> 'stopped'`, agentID, agentID).Scan(&publicationCount); err != nil {
			return nil, err
		}
		if publicationCount != 0 {
			return nil, errors.New("center: stop direct public publications before changing the public entry address")
		}
	}
	if !input.DirectPublic {
		input.PublicAddress = ""
		input.PublicBindAddress = ""
		input.PublicMode = ""
	} else {
		publicEgress, err := agentPublicEgress(ctx, tx, agentID)
		if err != nil {
			return nil, err
		}
		if publicEgress == nil || !lastSeenAt.After(s.now().UTC().Add(-agentConnectedMaxAge)) {
			return nil, errors.New("center: wait for the Agent to detect its current public egress address")
		}
		if input.PublicAddress != publicEgress.Address || input.PublicBindAddress != publicEgress.BindAddress || input.PublicMode != publicEgress.Mode {
			return nil, errors.New("center: public ingress must match the Agent's current public egress mapping")
		}
		input.PublicVerifiedAt = publicEgress.ObservedAt.UTC()
	}
	if err := networking.ValidateProfile(candidates, input); err != nil {
		return nil, err
	}
	if previous == nil || previous.ServiceAddress != input.ServiceAddress {
		var deploymentCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID).Scan(&deploymentCount); err != nil {
			return nil, err
		}
		if deploymentCount != 0 {
			return nil, errors.New("center: finish or recover deployment tasks before changing the private service address")
		}
		var applicationCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE node_id = ? AND status NOT IN ('stopped', 'failed')`, agentID).Scan(&applicationCount); err != nil {
			return nil, err
		}
		if applicationCount != 0 {
			return nil, errors.New("center: stop applications before changing the private service address")
		}
	}
	if previous != nil {
		addressChecks := []struct {
			changed bool
			kind    string
			label   string
		}{
			{changed: previous.LANAddress != input.LANAddress, kind: publicationLAN, label: "LAN"},
			{changed: previous.HeadscaleAddress != input.HeadscaleAddress, kind: publicationHeadscale, label: "Headscale"},
		}
		for _, check := range addressChecks {
			if !check.changed {
				continue
			}
			var publicationCount int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND kind = ? AND status <> 'stopped'`, agentID, check.kind).Scan(&publicationCount); err != nil {
				return nil, err
			}
			if publicationCount != 0 {
				return nil, fmt.Errorf("center: stop %s publications before changing this entry address", check.label)
			}
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
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id WHERE (a.node_id = ? OR p.gateway_node_id = ?) AND p.kind IN ('public_direct', 'public_shared_443') AND p.status <> 'stopped'`, agentID, agentID).Scan(&publicationCount); err != nil {
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
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND kind = ? AND status <> 'stopped'`, agentID, check.publication).Scan(&publicationCount); err != nil {
			return nil, err
		}
		if publicationCount != 0 {
			return nil, fmt.Errorf("center: stop %s publications before disabling this network", check.kind)
		}
	}
	enabledJSON, _ := json.Marshal(input.EnabledKinds)
	verifiedAt := ""
	if !input.PublicVerifiedAt.IsZero() {
		verifiedAt = input.PublicVerifiedAt.Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_profiles(agent_id, service_address, lan_address, headscale_address, public_address, public_bind_address, public_mode, enabled_kinds_json, direct_public, public_verified_at, confirmed_at, candidate_observed_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET service_address = excluded.service_address, lan_address = excluded.lan_address, headscale_address = excluded.headscale_address, public_address = excluded.public_address, public_bind_address = excluded.public_bind_address, public_mode = excluded.public_mode, enabled_kinds_json = excluded.enabled_kinds_json, direct_public = excluded.direct_public, public_verified_at = excluded.public_verified_at, confirmed_at = excluded.confirmed_at, candidate_observed_at = excluded.candidate_observed_at`, agentID, input.ServiceAddress, input.LANAddress, input.HeadscaleAddress, input.PublicAddress, input.PublicBindAddress, input.PublicMode, enabledJSON, input.DirectPublic, verifiedAt, input.ConfirmedAt.Format(time.RFC3339Nano), input.CandidateObserved.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("center: save network profile: %w", err)
	}
	if err := s.autoAssignFirstSiteGateway(ctx, tx, agentID, s.now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.reconcileBuiltinHeadscaleDNSIfConfigured(ctx); err != nil {
		return nil, err
	}
	if err := s.queueAllGatewayStates(ctx); err != nil {
		return nil, err
	}
	return networkProfile(ctx, s.db, agentID)
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
