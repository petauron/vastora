package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/xrayrecovery"
)

func TestRetainedXrayInspectionRemainsReadOnlyAfterControlRetirement(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "xray-recovery", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,runtime,role,status,created_at,updated_at) VALUES('xray-app','Xray',?,(SELECT site_id FROM agents WHERE id=?),'vastora-official/3x-ui','docker','worker','failed',?,?)`, node.ID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	result := xrayrecovery.Result{Action: "inspect", ApplicationID: "xray-app", RuntimeSHA256: digestFixture('a'), AgentSHA256: digestFixture('b'), AgentRevision: 7}
	raw, _ := json.Marshal(result)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO xray_configuration_recoveries(agent_id,application_id,id,action,state,result_json,created_at,updated_at) VALUES(?,'xray-app','xray-recovery-retained','inspect','awaiting_decision',?,?,?)`, node.ID, raw, now, now); err != nil {
		t.Fatal(err)
	}
	view, err := store.XrayConfigurationRecovery(ctx, node.ID)
	if err != nil || view.Result == nil || view.Result.AgentRevision != 7 || view.Result.RuntimeSHA256 != result.RuntimeSHA256 {
		t.Fatal("retained inspection evidence changed", err)
	}
	before, _ := json.Marshal(view)
	server := NewServer(store, t.TempDir(), false).Handler()
	for _, method := range []string{"GET", "POST"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/agents/"+node.ID+"/xray-configuration-recovery/inspect", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired recovery route returned %d", response.Code)
		}
	}
	view, err = store.XrayConfigurationRecovery(ctx, node.ID)
	after, _ := json.Marshal(view)
	if err != nil || string(before) != string(after) {
		t.Fatal("retired HTTP access mutated history", err)
	}
}

func TestRetiredXrayRecoveryDoesNotCreateWorkDuringHistoricalCutover(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='verify',subscription_authority='meridian' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var nodeID string
	if err := store.db.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.XrayConfigurationRecovery(ctx, nodeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("reading history created a recovery task", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM xray_configuration_recoveries WHERE agent_id=?`, nodeID).Scan(&count); err != nil || count != 0 {
		t.Fatal("retired recovery was scheduled", err)
	}
}

func digestFixture(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
