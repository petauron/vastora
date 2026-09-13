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

func TestLandingPrepareCreatesAttachedExpiredChildAndConfirmsDisable(t *testing.T) {
	for _, ignoreDisable := range []bool{false, true} {
		name := "confirmed"
		if ignoreDisable {
			name = "disable-not-confirmed"
		}
		t.Run(name, func(t *testing.T) {
			store, state, _ := landingSubscriptionTestState(t)
			grant := state.Grants["grant-a"]
			task := grant.Task
			task.Phase, task.Mode = "prepare", task.Grant.Mode
			delete(state.Grants, "grant-a")
			var child map[string]json.RawMessage
			var ids []int
			adds := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var value any
				switch {
				case r.Method == "GET" && r.URL.Path == "/panel/api/clients/get/Phone":
					value = map[string]any{"client": map[string]any{"id": "11111111-2222-4333-8444-555555555555"}, "inboundIds": []int{9}}
				case r.Method == "GET" && r.URL.Path == "/panel/api/inbounds/get/9":
					value = map[string]any{"id": 9, "protocol": "vless", "tag": "business"}
				case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/panel/api/clients/list/paged"):
					items := []any{}
					if child != nil {
						items = append(items, map[string]any{"email": task.Grant.FixedUser, "inboundIds": ids})
					}
					value = map[string]any{"items": items, "total": len(items)}
				case r.Method == "POST" && r.URL.Path == "/panel/api/clients/add":
					var input struct {
						Client     map[string]json.RawMessage `json:"client"`
						InboundIDs []int                      `json:"inboundIds"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					adds++
					if !slices.Equal(input.InboundIDs, []int{task.InboundID}) {
						t.Error("child must be created on exactly the validated inbound")
						_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "msg": "at least one inbound is required"})
						return
					}
					child, ids = input.Client, input.InboundIDs
					if string(child["expiryTime"]) != "1" || string(child["enable"]) != "false" || string(child["reset"]) != "0" {
						t.Error("creation lost the expired, non-renewing initial limits")
					}
					// Match 3x-ui v3.7.0, which overrides false during Create.
					setClientJSONField(child, "enable", true)
					value = map[string]any{"nodePending": false}
				case r.Method == "GET" && r.URL.Path == "/panel/api/clients/get/"+task.Grant.FixedUser:
					value = map[string]any{"client": child, "inboundIds": ids}
				case r.Method == "POST" && r.URL.Path == "/panel/api/clients/update/"+task.Grant.FixedUser:
					if err := json.NewDecoder(r.Body).Decode(&child); err != nil {
						t.Error(err)
					}
					if ignoreDisable {
						setClientJSONField(child, "enable", true)
					}
					value = map[string]any{"nodePending": false}
				default:
					t.Error("unexpected preparation operation", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": value})
			}))
			defer server.Close()
			connection := threeXUIClientTestStore(t, server, "native-token")
			defer connection.Close()
			installation, err := connection.AppliedInstallation(context.Background(), threeXUIKey)
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
			_, err = applyLandingClientCommand(context.Background(), store, task)
			if ignoreDisable {
				if err == nil || !strings.Contains(err.Error(), "child disable was not confirmed") {
					t.Fatalf("unconfirmed disable accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if adds != 1 {
				t.Fatalf("created %d children", adds)
			}
			if !ignoreDisable && (string(child["enable"]) != "false" || string(child["expiryTime"]) != "1") {
				t.Fatal("prepared child is usable before activation")
			}
		})
	}
}
