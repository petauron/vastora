package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingQuotaDoesNotReleaseBudgetBeforeDecreasesAreConfirmed(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "resume"
		if stop {
			name = "blocked-during-partial-write"
		}
		t.Run(name, func(t *testing.T) {
			store, state, parent := landingSubscriptionTestState(t)
			grant := state.Grants["grant-a"]
			child := grant.Task.Grant.FixedIdentity
			account := state.Accounts[parent]
			account.Limits = []landing.QuotaLimit{{ID: parent, Total: 300, Enabled: true}, {ID: child, Total: 700, Enabled: true}}
			// Deliberately list the increase first: execution must still decrease all
			// old allocations before letting another identity spend the same budget.
			account.PendingLimits = []landing.QuotaLimit{{ID: parent, Total: 600, Enabled: true}, {ID: child, Total: 400, Enabled: true}}
			account.PendingExpiry = account.Expiry
			state.Accounts[parent] = account
			type client struct {
				UUID   string
				Fields map[string]json.RawMessage
			}
			clients := map[string]*client{}
			for _, value := range []struct {
				email, uuid string
				total       int64
			}{{account.Email, "11111111-2222-4333-8444-555555555555", 300}, {grant.Task.Grant.FixedUser, grant.Task.FixedUUID, 700}} {
				fields := map[string]json.RawMessage{}
				for key, field := range map[string]any{"id": value.uuid, "email": value.email, "totalGB": value.total, "enable": true, "expiryTime": account.Expiry, "reset": 0} {
					fields[key], _ = json.Marshal(field)
				}
				clients[value.email] = &client{UUID: value.uuid, Fields: fields}
			}
			pending := true
			writes := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer native-token" {
					t.Error("native auth missing")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch {
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/panel/api/clients/get/"):
					item := clients[strings.TrimPrefix(r.URL.Path, "/panel/api/clients/get/")]
					if item == nil {
						t.Error("unexpected client read")
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": map[string]any{"client": item.Fields, "inboundIds": []int{9}}})
				case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/panel/api/clients/update/"):
					email := strings.TrimPrefix(r.URL.Path, "/panel/api/clients/update/")
					item := clients[email]
					if item == nil {
						t.Error("unexpected client write")
						w.WriteHeader(http.StatusNotFound)
						return
					}
					var fields map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || clientJSONText(fields, "id") != item.UUID {
						t.Error("quota write changed identity", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					writes = append(writes, email)
					item.Fields = fields // Controller DB changes even while worker is pending.
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": map[string]bool{"nodePending": pending && email == grant.Task.Grant.FixedUser}})
				default:
					t.Error("quota must not reset upstream traffic or restart arbitrary services")
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			if err := store.saveLandingController(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if err := store.applyLandingQuotaPlan(context.Background(), server.URL, "native-token", state, parent); err == nil {
				t.Fatal("pending worker was accepted")
			}
			if !reflect.DeepEqual(writes, []string{grant.Task.Grant.FixedUser}) {
				t.Fatal("new allocation was released before reduction was confirmed", writes)
			}
			// Recover from the encrypted journal, not the mutated in-memory value.
			recovered, err := store.landingController(context.Background())
			if err != nil || !reflect.DeepEqual(recovered.Accounts[parent].PendingLimits, account.PendingLimits) || !reflect.DeepEqual(recovered.Accounts[parent].Limits, account.Limits) {
				t.Fatal("pending worker response committed or lost the allocation checkpoint", err)
			}
			pending = false
			if stop {
				a := recovered.Accounts[parent]
				a.Blocked = true
				recovered.Accounts[parent] = a
				if err := store.saveLandingController(context.Background(), recovered); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.applyLandingQuotaPlan(context.Background(), server.URL, "native-token", recovered, parent); err != nil {
				t.Fatal(err)
			}
			wantWrites := []string{grant.Task.Grant.FixedUser, grant.Task.Grant.FixedUser, account.Email}
			wantLimits := slices.Clone(account.PendingLimits)
			if stop {
				wantWrites = []string{grant.Task.Grant.FixedUser, account.Email, grant.Task.Grant.FixedUser}
				for i := range wantLimits {
					wantLimits[i].Enabled = false
					var enabled bool
					email := account.Email
					if wantLimits[i].ID == child {
						email = grant.Task.Grant.FixedUser
					}
					if json.Unmarshal(clients[email].Fields["enable"], &enabled) != nil || enabled {
						t.Fatal("partial replay re-enabled a blocked identity")
					}
				}
			}
			if !reflect.DeepEqual(writes, wantWrites) {
				t.Fatal("retry did not preserve reduction-before-increase ordering", writes)
			}
			recovered, err = store.landingController(context.Background())
			if err != nil || len(recovered.Accounts[parent].PendingLimits) != 0 || !reflect.DeepEqual(recovered.Accounts[parent].Limits, wantLimits) || !reflect.DeepEqual(recovered.Accounts[parent].Members, account.Members) {
				t.Fatal("confirmed quota replay changed counters or failed to commit", err)
			}
		})
	}
}

func TestLandingChildAgentRejectsUnscopedCommandsBeforeNativeAccess(t *testing.T) {
	for _, action := range []string{"create", "update", "set_enabled", "delete", "reset_traffic", "reveal_link", "reveal_subscription"} {
		_, err := applyThreeXUIClientCommand(context.Background(), nil, ThreeXUIClientCommandTask{Action: action, Email: landing.FixedUser("child"), NewEmail: landing.FixedUser("child")})
		if err == nil || !strings.Contains(err.Error(), "scoped landing") {
			t.Fatal("ordinary task can bypass managed child lifecycle", action, err)
		}
	}
}
