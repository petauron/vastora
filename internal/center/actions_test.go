package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestActionsAreLimitedByStoreAndHTTPAPI(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	base := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 120; index++ {
		createdAt := base.Add(time.Duration(index) * time.Second)
		store.now = func() time.Time { return createdAt }
		if err := store.recordStandaloneTaskEvent(ctx, fmt.Sprintf("task-%03d", index), node.ID, "application.apply", int64(index), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
	}

	for _, test := range []struct {
		name  string
		limit int
		want  int
	}{
		{name: "requested", limit: 3, want: 3},
		{name: "default", limit: 0, want: defaultActionLimit},
		{name: "maximum", limit: 500, want: maxActionLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			actions, err := store.ListActions(ctx, test.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(actions) != test.want {
				t.Fatalf("ListActions(%d) returned %d actions, want %d", test.limit, len(actions), test.want)
			}
		})
	}

	server := NewServer(store, "", false)
	response := httptest.NewRecorder()
	server.handleListActions(response, httptest.NewRequest(http.MethodGet, "/api/v1/actions?limit=2", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("limited actions status = %d, body = %q", response.Code, response.Body.String())
	}
	var payload struct {
		Actions []ActionView `json:"actions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Actions) != 2 {
		t.Fatalf("HTTP limit returned %d actions, want 2", len(payload.Actions))
	}

	for _, target := range []string{"/api/v1/actions?limit=0", "/api/v1/actions?limit=invalid"} {
		response = httptest.NewRecorder()
		server.handleListActions(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid limit %q status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
}
