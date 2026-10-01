package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

type AgentReinstallNetworkReview struct {
	Previous        *networking.Profile            `json:"previous,omitempty"`
	Candidates      []networking.Candidate         `json:"candidates"`
	PublicEgress    *networking.PublicEgress       `json:"publicEgress,omitempty"`
	PrivatePeer     *landing.PeerIdentity          `json:"privatePeer,omitempty"`
	Ready           bool                           `json:"ready"`
	Approval        *AgentReinstallNetworkApproval `json:"approval,omitempty"`
	ApprovalCurrent bool                           `json:"approvalCurrent"`
}

// Address approval is durable intent for application restoration. It does not
// activate a profile, rewrite a publication, or release the execution fence.
type AgentReinstallNetworkApproval struct {
	PlanRevision       string                `json:"planRevision"`
	Previous           *networking.Profile   `json:"previous,omitempty"`
	Profile            networking.Profile    `json:"profile"`
	PrivatePeer        *landing.PeerIdentity `json:"privatePeer,omitempty"`
	ControllerID       string                `json:"controllerId,omitempty"`
	ControllerEndpoint string                `json:"controllerEndpoint,omitempty"`
	AuthorizedBy       string                `json:"authorizedBy"`
	ApprovedAt         time.Time             `json:"approvedAt"`
}

type AgentReinstallNetworkInput struct {
	OperationID      string             `json:"operationId"`
	PlanRevision     string             `json:"planRevision"`
	ConfirmMigration bool               `json:"confirmMigration"`
	Profile          networking.Profile `json:"profile"`
}

func recordReinstallNetworkObservation(ctx context.Context, tx *sql.Tx, agentID string, runtime *landing.ClientRuntime, ownership string, now time.Time) error {
	encoded := []byte(`{}`)
	if ownership == "managed" && runtime != nil && runtime.Generation == landing.ClientRuntimeGeneration && runtime.Peer.ID != "" && strings.TrimPrefix(runtime.Peer.PublicKey, "nodekey:") != "" && (landing.ServerPlan{Revision: 1, Address: runtime.Peer.Address}).Validate() == nil {
		encoded, _ = json.Marshal(runtime.Peer)
	}
	// This record is authenticated by the replacement heartbeat, separately from
	// the capability table, which requires an already active network profile.
	_, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET replacement_peer_json=?,replacement_network_observed_at=? WHERE agent_id=? AND state='review_required' AND replacement_fingerprint<>''`, encoded, now.UTC().Format(time.RFC3339Nano), agentID)
	return err
}

func (s *Store) agentReinstallNetworkReview(ctx context.Context, tx *sql.Tx, agentID string) (*AgentReinstallNetworkReview, error) {
	var previous, peer, approval, key []byte
	var observed, seen, expected, status, revoked, ownership string
	err := tx.QueryRowContext(ctx, `SELECT r.profile_json,op.replacement_peer_json,(SELECT approval_json FROM agent_reinstall_network_approvals WHERE operation_id=op.id ORDER BY rowid DESC LIMIT 1),op.replacement_network_observed_at,op.replacement_fingerprint,a.x25519_public_key,a.last_seen_at,a.status,a.credential_revoked_at,a.tailscale_ownership
 FROM agent_reinstall_operations op JOIN agents a ON a.id=op.agent_id LEFT JOIN agent_network_profile_recovery r ON r.agent_id=a.id
 WHERE op.agent_id=? AND op.state='review_required'`, agentID).Scan(&previous, &peer, &approval, &observed, &expected, &key, &seen, &status, &revoked, &ownership)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	review := &AgentReinstallNetworkReview{}
	if len(previous) > 0 {
		if err = json.Unmarshal(previous, &review.Previous); err != nil {
			return nil, err
		}
	}
	if len(approval) > 0 {
		if err = json.Unmarshal(approval, &review.Approval); err != nil {
			return nil, err
		}
	}
	cutoff := s.now().UTC().Add(-agentConnectedMaxAge)
	observedAt, _ := time.Parse(time.RFC3339Nano, observed)
	seenAt, _ := time.Parse(time.RFC3339Nano, seen)
	digest := sha256.Sum256(key)
	review.Ready = status == "active" && revoked == "" && expected == hex.EncodeToString(digest[:]) && observedAt.After(cutoff) && seenAt.After(cutoff)
	review.Candidates, err = networkCandidates(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	egress, err := agentPublicEgress(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if egress != nil && egress.ObservedAt.After(cutoff) {
		review.PublicEgress = egress
	}
	var identity landing.PeerIdentity
	if json.Unmarshal(peer, &identity) != nil {
		return nil, errors.New("center: saved replacement private identity is invalid")
	}
	if review.Ready && ownership == "managed" && identity.ID != "" {
		review.PrivatePeer = &identity
	}
	if review.Approval != nil {
		_, err := validateReinstallProfile(review, normalizeReinstallProfile(review.Approval.Profile))
		review.ApprovalCurrent = err == nil && (review.Approval.PrivatePeer == nil || reflect.DeepEqual(review.Approval.PrivatePeer, review.PrivatePeer))
	}
	return review, nil
}

func normalizeReinstallProfile(profile networking.Profile) networking.Profile {
	profile.ServiceAddress = strings.TrimSpace(profile.ServiceAddress)
	profile.LANAddress = strings.TrimSpace(profile.LANAddress)
	profile.HeadscaleAddress = strings.TrimSpace(profile.HeadscaleAddress)
	profile.PublicAddress = strings.TrimSpace(profile.PublicAddress)
	profile.PublicBindAddress = strings.TrimSpace(profile.PublicBindAddress)
	profile.PublicMode = strings.TrimSpace(profile.PublicMode)
	profile.EnabledKinds = uniqueStrings(profile.EnabledKinds)
	sort.Strings(profile.EnabledKinds)
	profile.ConfirmedAt = time.Time{}
	profile.CandidateObserved = time.Time{}
	profile.PublicVerifiedAt = time.Time{}
	return profile
}

func validateReinstallProfile(review *AgentReinstallNetworkReview, profile networking.Profile) (networking.Profile, error) {
	if review == nil || !review.Ready {
		return profile, errors.New("center: wait for a fresh network observation from the replacement machine")
	}
	// Migration preserves the enabled networks. Removing dependencies is a
	// different business change and cannot be hidden in identity recovery.
	if review.Previous != nil {
		for _, kind := range review.Previous.EnabledKinds {
			if !slices.Contains(profile.EnabledKinds, kind) {
				return profile, errors.New("center: retain the previous enabled networks during address migration")
			}
		}
		if review.Previous.DirectPublic && !profile.DirectPublic {
			return profile, errors.New("center: retain the public entry mapping during address migration")
		}
	}
	if profile.DirectPublic {
		egress := review.PublicEgress
		if egress == nil || profile.PublicAddress != egress.Address || profile.PublicBindAddress != egress.BindAddress || profile.PublicMode != egress.Mode {
			return profile, errors.New("center: public ingress must match the replacement machine's fresh public egress mapping")
		}
		profile.PublicVerifiedAt = egress.ObservedAt
	}
	if err := networking.ValidateProfile(review.Candidates, profile); err != nil {
		return profile, err
	}
	if profile.HeadscaleAddress != "" && (review.PrivatePeer == nil || profile.HeadscaleAddress != review.PrivatePeer.Address) {
		return profile, errors.New("center: the selected private address needs the replacement machine's authenticated private identity")
	}
	return profile, nil
}

func (s *Store) ApproveAgentReinstallNetwork(ctx context.Context, agentID, adminID string, input AgentReinstallNetworkInput) (*AgentReinstallNetworkApproval, error) {
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	if !input.ConfirmMigration || input.OperationID == "" || len(input.PlanRevision) != 64 {
		return nil, errors.New("center: review and explicitly confirm the replacement network addresses")
	}
	input.Profile = normalizeReinstallProfile(input.Profile)
	plan, err := s.AgentReinstallPlan(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if plan.Recovery == nil || plan.Recovery.ID != input.OperationID || plan.Recovery.State != "review_required" || plan.Recovery.AuthorizedBy != adminID || plan.CredentialRevoked || plan.NetworkReview == nil {
		return nil, errors.New("center: network approval does not match this recovery authorization")
	}
	var admin bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return nil, err
	}
	if !admin {
		return nil, errors.New("center: administrator authorization required")
	}
	// A lost response may retrieve exactly the saved approval, without another
	// controller request or audit event. It never authorizes a different profile.
	if saved := plan.NetworkReview.Approval; saved != nil && saved.PlanRevision == input.PlanRevision && reflect.DeepEqual(normalizeReinstallProfile(saved.Profile), input.Profile) {
		return saved, nil
	}
	if plan.Revision != input.PlanRevision {
		return nil, errors.New("center: network recovery evidence changed; review it again before confirming")
	}
	profile, err := validateReinstallProfile(plan.NetworkReview, input.Profile)
	if err != nil {
		return nil, err
	}
	controllerID, controllerEndpoint := "", ""
	if profile.HeadscaleAddress != "" {
		controllerID, controllerEndpoint, err = s.verifyReinstallReplacementPeer(ctx, agentID, input.OperationID, *plan.NetworkReview.PrivatePeer)
		if err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The authenticated heartbeat or saved dependencies may change while the
	// controller is read. Recheck the entire reviewed snapshot before committing.
	current, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if current.Revision != plan.Revision || current.Recovery == nil || current.Recovery.ID != input.OperationID || current.Recovery.State != "review_required" || current.Recovery.AuthorizedBy != adminID || current.Recovery.PrivateIsolation == "pending" {
		return nil, errors.New("center: recovery changed during network inspection; review it again")
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return nil, err
	}
	if !admin {
		return nil, errors.New("center: administrator authorization required")
	}
	if current.PrivateNetwork.LandingRoutes > 0 && input.Profile.HeadscaleAddress == "" {
		return nil, errors.New("center: landing routes require the replacement private network address")
	}
	var missingEntry bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM publications WHERE entry_node_id=? AND status<>'stopped' AND
	 ((kind=? AND ?='') OR (kind=? AND ?='') OR (kind IN ('public_direct','public_shared_443') AND ?=0)))`, agentID, publicationHeadscale, input.Profile.HeadscaleAddress, publicationLAN, input.Profile.LANAddress, input.Profile.DirectPublic).Scan(&missingEntry); err != nil {
		return nil, err
	}
	if missingEntry {
		return nil, errors.New("center: retain the networks used by saved access entries during recovery")
	}
	profile, err = validateReinstallProfile(current.NetworkReview, input.Profile)
	if err != nil {
		return nil, err
	}
	if profile.HeadscaleAddress != "" {
		var shared bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_network_profiles WHERE agent_id<>? AND headscale_address=?)`, agentID, profile.HeadscaleAddress).Scan(&shared); err != nil {
			return nil, err
		}
		if shared {
			return nil, errors.New("center: replacement private address is assigned to another node")
		}
	}
	approved := &AgentReinstallNetworkApproval{PlanRevision: input.PlanRevision, Previous: current.NetworkReview.Previous, Profile: profile, ControllerID: controllerID, ControllerEndpoint: controllerEndpoint, AuthorizedBy: adminID, ApprovedAt: s.now().UTC()}
	if profile.HeadscaleAddress != "" {
		approved.PrivatePeer = current.NetworkReview.PrivatePeer
	}
	encoded, err := json.Marshal(approved)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_network_approvals(operation_id,plan_revision,approval_json) VALUES(?,?,?)`, input.OperationID, input.PlanRevision, encoded); err != nil {
		return nil, err
	}
	// Each explicit re-approval keeps its full previous/new-address evidence.
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET updated_at=? WHERE id=?`, approved.ApprovedAt.Format(time.RFC3339Nano), input.OperationID); err != nil {
		return nil, err
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, agentID, "agent.reinstall.network", 0, "succeeded", "Address migration approved; application restoration and business verification remain pending"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return approved, nil
}

func (s *Store) verifyReinstallReplacementPeer(ctx context.Context, agentID, operationID string, peer landing.PeerIdentity) (string, string, error) {
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT private_identity_json FROM agent_reinstall_operations WHERE id=? AND agent_id=? AND private_isolation IN ('withdrawn','not_required')`, operationID, agentID).Scan(&encoded); err != nil {
		return "", "", err
	}
	var previous reinstallPrivateIdentity
	if json.Unmarshal(encoded, &previous) != nil {
		return "", "", errors.New("center: saved previous private identity is invalid")
	}
	key := strings.TrimPrefix(peer.PublicKey, "nodekey:")
	if key == "" || key == strings.TrimPrefix(previous.NodeKey, "nodekey:") {
		return "", "", errors.New("center: replacement cannot reuse the previous private identity")
	}
	client, err := s.headscale(ctx)
	if err != nil {
		return "", "", errors.New("center: private controller is unavailable; network approval remains pending")
	}
	if previous.Endpoint != "" && previous.Endpoint != client.baseURL {
		return "", "", errors.New("center: private controller changed after recovery authorization")
	}
	var response struct {
		Nodes []removalHeadscaleNode `json:"nodes"`
	}
	if err = client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil || response.Nodes == nil {
		return "", "", errors.New("center: replacement private identity inspection failed")
	}
	id := ""
	for _, node := range response.Nodes {
		nodeKey := strings.TrimPrefix(node.NodeKey, "nodekey:")
		if previous.ID != "" && node.ID == previous.ID || previous.NodeKey != "" && nodeKey == strings.TrimPrefix(previous.NodeKey, "nodekey:") {
			return "", "", errors.New("center: previous private identity is still present")
		}
		if nodeKey != key && !slices.Contains(node.IPAddresses, peer.Address) {
			continue
		}
		if nodeKey != key || !slices.Contains(node.IPAddresses, peer.Address) || !slices.Contains(node.Tags, "tag:vastora-agent") || !validHeadscaleNodePath("/api/v1/node/"+node.ID) || id != "" {
			return "", "", errors.New("center: replacement private identity or address ownership does not match")
		}
		id = node.ID
	}
	if id == "" {
		return "", "", errors.New("center: replacement private identity was not found in the controller")
	}
	return id, client.baseURL, nil
}

func (s *Server) handleApproveAgentReinstallNetwork(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallNetworkInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	approval, err := s.store.ApproveAgentReinstallNetwork(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, approval)
}
