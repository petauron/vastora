package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/realitytarget"
)

func validRealityCandidate(value realitytarget.Candidate) bool {
	return validRealityTargetProof(RealityCommandResult{
		TargetHost: value.TargetHost, TargetIP: value.TargetIP, ServerName: value.ServerName,
		NodeASN: value.NodeASN, TargetASN: value.TargetASN, CDNProvider: value.CDNProvider,
		TLS13: value.TLS13, X25519: value.X25519, HTTP2: value.HTTP2, CertificateValid: value.CertificateValid,
	})
}

func validRealityVerification(input RealityCommandTask, result RealityCommandResult) bool {
	if result.Action != "verify" {
		return false
	}
	if !input.Recommend {
		return result.TargetHost == input.TargetHost && result.ServerName == input.ServerName && validRealityTargetProof(result) && len(result.Candidates) == 0
	}
	if len(result.Candidates) == 0 || len(result.Candidates) > len(realitytarget.Hosts()) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range result.Candidates {
		if !slices.Contains(realitytarget.Hosts(), value.TargetHost) || value.ServerName != value.TargetHost || seen[value.TargetHost] || !validRealityCandidate(value) || value.Samples != 3 || value.LatencyMillis < 1 {
			return false
		}
		seen[value.TargetHost] = true
	}
	return true
}

// Bind selection to a fresh completed check from the application node, not the
// controller's resolver. Creating an inbound never resolves a different IP.
func (s *Store) selectedRealityTarget(ctx context.Context, tx *sql.Tx, input RealityCommandInput, nodeID, privateAddress, publicAddress string) (*realitytarget.Candidate, error) {
	var requestJSON, resultJSON []byte
	var checkedAt string
	err := tx.QueryRowContext(ctx, `SELECT input_json, result_json, updated_at FROM application_commands
		WHERE id = ? AND application_id = ? AND agent_id = ? AND kind = ? AND state = 'succeeded'`,
		input.VerificationID, input.ApplicationID, nodeID, realityVerifyCommandKind).Scan(&requestJSON, &resultJSON, &checkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: select a target from a completed node-side check before creating REALITY")
	}
	if err != nil {
		return nil, err
	}
	var request RealityCommandTask
	var result RealityCommandResult
	var currentKey []byte
	if err := tx.QueryRowContext(ctx, `SELECT x25519_public_key FROM agents WHERE id = ? AND status = 'active'`, nodeID).Scan(&currentKey); err != nil {
		return nil, err
	}
	checked, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil || !checked.After(s.now().Add(-15*time.Minute)) || checked.After(s.now().Add(time.Minute)) ||
		json.Unmarshal(requestJSON, &request) != nil || json.Unmarshal(resultJSON, &result) != nil ||
		request.TargetApplicationID != input.ApplicationID || len(currentKey) == 0 || !bytes.Equal(currentKey, request.TargetAgentPublicKey) || request.TargetAddress != privateAddress || request.TargetPublicAddress != publicAddress || !validRealityVerification(request, result) {
		return nil, errors.New("center: target verification is stale or the node network changed; check again")
	}
	candidates := result.Candidates
	if !request.Recommend {
		var value realitytarget.Candidate
		if err := json.Unmarshal(resultJSON, &value); err != nil {
			return nil, err
		}
		candidates = []realitytarget.Candidate{value}
	}
	for _, value := range candidates {
		if value.TargetHost == input.TargetHost && value.ServerName == input.ServerName && value.TargetIP == input.TargetIP && validRealityCandidate(value) {
			return &value, nil
		}
	}
	return nil, errors.New("center: the selected address is not in the verified node-side results")
}
