package center

import (
	"context"
	"net/http"
)

type AgentReinstallClientCheckView struct {
	Protocol    string `json:"protocol"`
	AccountName string `json:"accountName"`
	EgressName  string `json:"egressName"`
	AgentReinstallClientCheck
	ApplicationID   string `json:"applicationId"`
	VerifierAgentID string `json:"verifierAgentId"`
}

type AgentReinstallClientCheckInput struct {
	RequestID       string `json:"requestId"`
	OperationID     string `json:"operationId"`
	PlanRevision    string `json:"planRevision"`
	ApplicationID   string `json:"applicationId"`
	VerifierAgentID string `json:"verifierAgentId"`
}

// Keep evidence outside plan construction: proof validation reconstructs that
// plan, so embedding this reader in the plan would introduce recursion.
func (s *Store) AgentReinstallClientChecks(ctx context.Context, target string) ([]AgentReinstallClientCheckView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.application_id,c.agent_id,json_extract(a.input_json,'$.protocol'),COALESCE(account.display_name,''),COALESCE(egress.name,'') FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id JOIN agent_reinstall_operations op ON op.id=a.operation_id LEFT JOIN meridian_credentials credential ON credential.id=json_extract(a.input_json,'$.credentialId') LEFT JOIN meridian_accounts account ON account.id=credential.account_id LEFT JOIN agents egress ON egress.id=json_extract(a.input_json,'$.egressId') WHERE op.agent_id=? AND op.state='review_required' ORDER BY c.created_at,c.id`, target)
	if err != nil {
		return nil, err
	}
	results := []AgentReinstallClientCheckView{}
	for rows.Next() {
		var v AgentReinstallClientCheckView
		if err = rows.Scan(&v.CommandID, &v.ApplicationID, &v.VerifierAgentID, &v.Protocol, &v.AccountName, &v.EgressName); err != nil {
			rows.Close()
			return nil, err
		}
		results = append(results, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].AgentReinstallClientCheck, err = s.readReinstallClientCheck(ctx, tx, results[i].VerifierAgentID, results[i].CommandID)
		if err != nil {
			return nil, err
		}
	}
	return results, tx.Commit()
}

func (s *Server) handleAgentReinstallClientChecks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		results, err := s.store.AgentReinstallClientChecks(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, results)
		return
	}
	var input AgentReinstallClientCheckInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallClientChecks(r.Context(), r.PathValue("id"), input.VerifierAgentID, admin, input.RequestID, AgentReinstallApplicationInput{OperationID: input.OperationID, PlanRevision: input.PlanRevision, ApplicationID: input.ApplicationID})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
