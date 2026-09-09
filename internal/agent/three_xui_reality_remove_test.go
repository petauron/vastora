package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoveLocalRealityReadbackAndRetry(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost-response"}[lostResponse], func(t *testing.T) {
			remoteID := 7
			inbounds := []threeXUIRealityInbound{{ID: 9, Tag: "local-node", Protocol: "vless", StreamSettings: json.RawMessage(`{"security":"reality"}`)}, {ID: 10, Tag: "remote-node", NodeID: &remoteID, Protocol: "vless"}}
			deletes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /panel/api/inbounds/list":
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": inbounds})
				case "POST /panel/api/inbounds/del/9":
					deletes++
					inbounds = inbounds[1:]
					if lostResponse {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{"success":true}`))
				default:
					t.Errorf("unexpected API mutation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			command := RealityCommandTask{Action: "remove", ServiceID: "local-service", InboundID: 9, InboundTag: "local-node"}
			for range 2 {
				result, err := removeThreeXUIRealityInbound(context.Background(), server.URL, "token", command)
				if err != nil || result.Action != "remove" || result.InboundID != 9 || result.InboundTag != "local-node" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			}
			if deletes != 1 || len(inbounds) != 1 || inbounds[0].ID != 10 {
				t.Fatal("retry deleted an unrelated inbound")
			}
		})
	}
}

func TestRemoveLocalRealityRejectsChangedIdentity(t *testing.T) {
	remoteID := 7
	for name, inbound := range map[string]threeXUIRealityInbound{
		"replacement": {ID: 9, Tag: "replacement", Protocol: "vless", StreamSettings: json.RawMessage(`{"security":"reality"}`)},
		"remote":      {ID: 9, Tag: "local-node", NodeID: &remoteID, Protocol: "vless", StreamSettings: json.RawMessage(`{"security":"reality"}`)},
		"protocol":    {ID: 9, Tag: "local-node", Protocol: "vmess", StreamSettings: json.RawMessage(`{"security":"reality"}`)},
		"security":    {ID: 9, Tag: "local-node", Protocol: "vless", StreamSettings: json.RawMessage(`{"security":"tls"}`)},
		"changed-id":  {ID: 11, Tag: "local-node", Protocol: "vless", StreamSettings: json.RawMessage(`{"security":"reality"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("unsafe delete")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []threeXUIRealityInbound{inbound}})
			}))
			defer server.Close()
			_, err := removeThreeXUIRealityInbound(context.Background(), server.URL, "token", RealityCommandTask{Action: "remove", ServiceID: "local-service", InboundID: 9, InboundTag: "local-node"})
			if err == nil {
				t.Fatal("unsafe identity was accepted")
			}
		})
	}
}

func TestRemoveLocalRealityDefersUnknownReadback(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			deleted = true
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if deleted {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"local-node","protocol":"vless","streamSettings":{"security":"reality"}}]}`))
	}))
	defer server.Close()
	_, err := removeThreeXUIRealityInbound(context.Background(), server.URL, "token", RealityCommandTask{Action: "remove", ServiceID: "local-service", InboundID: 9, InboundTag: "local-node"})
	if !realityMutationOutcomeUncertain(err) {
		t.Fatalf("unknown removal incorrectly finalized: %v", err)
	}
}

func TestRemoveLocalRealityRequiresAnExplicitEmptyList(t *testing.T) {
	for _, payload := range []string{`{"success":true,"obj":null}`, `{"success":true,"obj":[]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Error("unexpected deletion")
			}
			_, _ = w.Write([]byte(payload))
		}))
		_, err := removeThreeXUIRealityInbound(context.Background(), server.URL, "token", RealityCommandTask{Action: "remove", ServiceID: "local-service", InboundID: 9, InboundTag: "local-node"})
		server.Close()
		if payload == `{"success":true,"obj":null}` && !realityMutationOutcomeUncertain(err) {
			t.Fatalf("null list was treated as deletion proof: %v", err)
		}
		if payload == `{"success":true,"obj":[]}` && err != nil {
			t.Fatalf("explicit empty list rejected: %v", err)
		}
	}
}
