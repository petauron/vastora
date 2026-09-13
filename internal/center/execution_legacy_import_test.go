package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

func TestLegacyReceiptImportArchivesWithoutExecuting(t *testing.T) {
	for _, state := range []string{"processing", "completed", "acknowledged"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "legacy-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
			session := "legacy-import-original-process-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			receipt := controlplane.LegacyReceipt{TaskID: "old-task", Kind: "application.apply", Attempt: 1, TaskHash: make([]byte, 32), State: state, CreatedAt: stamp, UpdatedAt: stamp}
			if state != "processing" {
				receipt.Completion = json.RawMessage(`{"taskId":"old-task","attempt":1,"result":{"secrets":{"password":"synthetic-private-value"}},"oldField":true}`)
			}
			raw, digest, err := controlplane.EncodeLegacyReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			input := controlplane.LegacyReceiptImport{SessionID: session, Digest: digest, Receipt: receipt}
			if _, err := store.ImportLegacyReceipt(ctx, node.ID, "wrong-credential", input); err == nil {
				t.Fatal("unauthenticated import accepted")
			}
			wrong := input
			wrong.SessionID = "wrong-session"
			if _, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, wrong); err == nil {
				t.Fatal("wrong session accepted")
			}
			id, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, input)
			if err != nil {
				t.Fatal(err)
			}
			var sealed []byte
			var phase string
			if err := store.db.QueryRow(`SELECT sealed_task,phase FROM task_executions WHERE id=?`, id).Scan(&sealed, &phase); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(sealed, []byte("synthetic-private-value")) {
				t.Fatal("archive stored plaintext credentials")
			}
			opened, err := secret.Open(store.key, sealed, []byte("execution-task:"+id))
			if err != nil || !bytes.Equal(opened, raw) || phase != "legacy:"+state {
				t.Fatal("archive changed original evidence")
			}
			if again, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, input); err != nil || again != id {
				t.Fatal("identical archival not acknowledged")
			}
			conflict := input
			conflict.Receipt.Kind = "different"
			_, conflict.Digest, _ = controlplane.EncodeLegacyReceipt(conflict.Receipt)
			if _, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, conflict); err == nil {
				t.Fatal("changed evidence overwrote archive")
			}
			err = store.executionClaimAllowed(ctx, node.ID, session)
			if state == "acknowledged" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("unknown receipt did not fence work: %v", err)
			}
			views, err := executionViewsForTest(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			viewJSON, _ := json.Marshal(views)
			if len(views) != 1 || strings.Contains(string(viewJSON), "synthetic-private-value") {
				t.Fatal("duplicate archive or credential exposure")
			}
			inspection, err := store.InspectLegacyReceipt(ctx, id)
			if err != nil || inspection.TaskID != receipt.TaskID || inspection.Kind != receipt.Kind || inspection.Attempt != receipt.Attempt || inspection.State != state || inspection.HasCompletion != (len(receipt.Completion) > 0) {
				t.Fatalf("legacy inspection lost task identity: %+v %v", inspection, err)
			}
			inspectionJSON, _ := json.Marshal(inspection)
			if bytes.Contains(inspectionJSON, []byte("synthetic-private-value")) || bytes.Contains(inspectionJSON, []byte("oldField")) {
				t.Fatal("inspection exposed completion contents")
			}
			unauthenticated := httptest.NewRecorder()
			NewServer(store, "", false).Handler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/executions/"+id+"/legacy-receipt", nil))
			if unauthenticated.Code != http.StatusUnauthorized {
				t.Fatalf("inspection accepted unauthenticated request: %d", unauthenticated.Code)
			}
			cookie, _, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
			if err != nil {
				t.Fatal(err)
			}
			inspectRequest := httptest.NewRequest(http.MethodGet, "/api/v1/executions/"+id+"/legacy-receipt", nil)
			inspectRequest.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
			inspectResponse := httptest.NewRecorder()
			NewServer(store, "", false).Handler().ServeHTTP(inspectResponse, inspectRequest)
			if inspectResponse.Code != http.StatusOK || inspectResponse.Header().Get("Cache-Control") != "no-store" || bytes.Contains(inspectResponse.Body.Bytes(), []byte("synthetic-private-value")) {
				t.Fatalf("authenticated inspection violated response boundary: %d", inspectResponse.Code)
			}
			requestBody, _ := json.Marshal(input)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/legacy-receipts", bytes.NewReader(requestBody))
			r.Header.Set("Authorization", "Bearer "+node.Credential)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewServer(store, "", false).Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "synthetic-private-value") {
				t.Fatalf("archive acknowledgement: %d", w.Code)
			}
			if _, err := store.db.Exec(`UPDATE task_executions SET sealed_task=X'0001' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, input); err == nil {
				t.Fatal("corrupt archive acknowledged")
			}
			if _, err := store.InspectLegacyReceipt(ctx, id); err == nil {
				t.Fatal("corrupt archive inspected")
			}
			if _, err := store.db.Exec(`UPDATE task_executions SET sealed_task=?,digest='mismatched' WHERE id=?`, sealed, id); err != nil {
				t.Fatal(err)
			}
			if _, err := store.InspectLegacyReceipt(ctx, id); err == nil {
				t.Fatal("mismatched digest inspected")
			}
			if _, err := store.db.Exec(`UPDATE task_executions SET sealed_task=zeroblob(?) WHERE id=?`, controlplane.LegacyReceiptMaxPayloadBytes+1025, id); err != nil {
				t.Fatal(err)
			}
			if _, err := store.InspectLegacyReceipt(ctx, id); err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("oversized archive not rejected before decryption: %v", err)
			}
		})
	}
}
