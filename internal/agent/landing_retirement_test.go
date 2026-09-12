package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestLandingRetirementRequiresConfirmedDetachmentAndPreservesLedger(t *testing.T) {
	store, state, parent := landingSubscriptionTestState(t)
	grant := state.Grants["grant-a"]
	fields := map[string]json.RawMessage{}
	for key, value := range map[string]any{"id": grant.Task.FixedUUID, "email": grant.Task.Grant.FixedUser, "subId": grant.ChildSubscription, "enable": true, "expiryTime": 0, "reset": 0, "totalGB": 1000} {
		fields[key], _ = json.Marshal(value)
	}
	inboundIDs, pending, detachCalls := []int{9}, true, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch {
		case r.Method == "GET" && r.URL.Path == "/panel/api/clients/get/Phone":
			// The parent was externally removed; old counters must stay charged.
			w.WriteHeader(http.StatusNotFound)
			return
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/panel/api/clients/list/paged"):
			value = map[string]any{"items": []any{map[string]any{"email": grant.Task.Grant.FixedUser, "inboundIds": inboundIDs}}, "total": 1}
		case r.Method == "GET" && r.URL.Path == "/panel/api/clients/get/"+grant.Task.Grant.FixedUser:
			value = map[string]any{"client": fields, "inboundIds": inboundIDs}
		case r.Method == "POST" && r.URL.Path == "/panel/api/clients/update/"+grant.Task.Grant.FixedUser:
			if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
				t.Error(err)
			}
			value = map[string]any{"nodePending": false}
		case r.Method == "POST" && r.URL.Path == "/panel/api/clients/"+grant.Task.Grant.FixedUser+"/detach":
			var input struct {
				InboundIDs []int `json:"inboundIds"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || !slices.Equal(input.InboundIDs, []int{9}) {
				t.Error("detachment scope changed during retry")
			}
			inboundIDs, detachCalls = nil, detachCalls+1
			value = map[string]any{"nodePending": pending}
		default:
			t.Error("retirement touched an unrelated native resource", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": value})
	}))
	defer server.Close()
	connectionStore := threeXUIClientTestStore(t, server, "native-token")
	defer connectionStore.Close()
	installation, err := connectionStore.AppliedInstallation(context.Background(), threeXUIKey)
	if err != nil {
		t.Fatal(err)
	}
	installation.ApplicationID = state.ControllerID
	if _, err := store.RecordApplied(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if err := store.saveLandingController(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	task := grant.Task
	task.Phase, task.Mode, task.Grant.Enabled, task.Revision = "retire", grant.Task.Grant.Mode, false, 2
	if _, err := applyLandingClientCommand(context.Background(), store, task); err == nil {
		t.Fatal("pending detach accepted")
	}
	pending = false
	if _, err := applyLandingClientCommand(context.Background(), store, task); err != nil {
		t.Fatal(err)
	}
	if detachCalls != 2 {
		t.Fatal("lost response replay skipped the original detach scope")
	}
	recovered, err := store.landingController(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Grants[grant.Task.Grant.ID].Phase != "revoked" || recovered.Grants[grant.Task.Grant.ID].Material.FixedLink != "" || !recovered.Accounts[parent].Blocked || !slices.Equal(recovered.Accounts[parent].Members, state.Accounts[parent].Members) {
		t.Fatal("missing parent counters retained access or erased historical usage")
	}
}
