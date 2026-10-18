package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/senulun"
)

// Recovery tests retain the old registration evidence without making new
// installations depend on a Center-managed Senulun service.
func createHistoricalSenulunCollector(t *testing.T, s *Store, serviceID, collectorID string, options json.RawMessage) (DeploymentView, error) {
	t.Helper()
	ctx := context.Background()
	values := map[string]any{}
	if len(options) > 0 {
		if err := json.Unmarshal(options, &values); err != nil {
			t.Fatal(err)
		}
	}
	values["service_url"] = "https://senulun.private.example.com/"
	values["node_name"] = "collector"
	values["node_group"] = "default"
	values["enrollment_token"] = strings.Repeat("fixture-", 4)
	raw, _ := json.Marshal(values)
	deployment, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: collectorID, AppKey: senulunAgentAppKey, Config: raw})
	if err != nil {
		return deployment, err
	}
	delete(values, "enrollment_token")
	values["service_application_id"] = serviceID
	raw, _ = json.Marshal(values)
	if _, err = s.db.ExecContext(ctx, `UPDATE deployments SET config_json=?,secret_id=NULL WHERE id=?`, raw, deployment.ID); err != nil {
		return deployment, err
	}
	var serviceAgentID string
	if err = s.db.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id=?`, serviceID).Scan(&serviceAgentID); err != nil {
		return deployment, err
	}
	id, err := randomToken(18)
	if err != nil {
		return deployment, err
	}
	id = "application-command-" + id
	input, _ := json.Marshal(senulun.EnrollmentTask{ApplicationID: serviceID, DeploymentID: deployment.ID})
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, serviceID, serviceAgentID, collectorID, senulun.EnrollmentKind, input, now, now)
	return deployment, err
}
