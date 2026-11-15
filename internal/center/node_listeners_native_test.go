package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
)

func TestNativeSharedPublicationRequiresOwnedReadyResources(t *testing.T) {
	for _, tc := range []struct {
		name, runtime, field, value, endpoint string
		reject                                bool
	}{
		{name: "native", runtime: "native"},
		{name: "systemd", runtime: "systemd"},
		{name: "foreign application", runtime: "native", field: "applicationId", value: "another-app", reject: true},
		{name: "foreign package", runtime: "native", field: "appKey", value: "example/another", reject: true},
		{name: "wrong manifest", runtime: "native", field: "manifestSha256", value: strings.Repeat("b", 64), reject: true},
		{name: "wrong runtime", runtime: "docker", reject: true},
		{name: "not ready", runtime: "native", field: "state", value: "pending", reject: true},
		{name: "different backend", runtime: "native", endpoint: "10.0.0.62:10443", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			profile := networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
			node := enrollOrchestrationNode(t, store, "native-listener", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN}, {Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic}}, profile)
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			site := testSiteID(t, store)
			exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at) VALUES('native-app','Native',?,?,'example/native','','running','host','',?,?)`, node.ID, site, stamp, stamp)
			endpoint := tc.endpoint
			if endpoint == "" {
				endpoint = "10.0.0.61:10443"
			}
			exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,management,status,proxy_protocol,created_at,updated_at) VALUES('native-service','native-app',?,'proxy','tcp',10443,10443,?,'catalog',0,'ready','v2',?,?)`, site, endpoint, stamp, stamp)
			receipt := map[string]any{"version": 1, "applicationId": "native-app", "appKey": "example/native", "runtime": tc.runtime, "manifestSha256": strings.Repeat("a", 64), "state": "ready"}
			if tc.field != "" {
				receipt[tc.field] = tc.value
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO application_resources(application_id,package_revision,manifest_sha256,authorized_capabilities_json,resources_json,adoption_state,updated_at) VALUES('native-app',1,?,'[]',?,'ready',?)`, strings.Repeat("a", 64), raw, stamp)
			_, err = store.CreatePublication(ctx, PublicationInput{ServiceID: "native-service", Kind: publicationShared443, Ingress: PublicationIngress{Owner: ingressApplicationNode}, Hostname: "entry.example.test", SNIHostname: "entry.example.test", DNSProvider: "manual"})
			if tc.reject {
				if err == nil {
					t.Fatal("unowned or unavailable native backend was published")
				}
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM publications WHERE service_id='native-service'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed publication left state: count=%d err=%v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT desired_json FROM node_listener_states WHERE node_id=?`, node.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var state gateway.NodeListenerState
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Listener.Routes) != 1 {
				t.Fatalf("routes: %+v", state.Listener.Routes)
			}
			route := state.Listener.Routes[0]
			if state.Listener.Address != profile.PublicAddress || !state.Listener.RejectUnmatched || route.Hostname != "entry.example.test" || route.ProxyProtocol != "v2" || len(route.Upstreams) != 1 || route.Upstreams[0].Address != profile.ServiceAddress || route.Upstreams[0].Port != 10443 {
				t.Fatalf("wrong native listener: %+v", state)
			}
		})
	}
}
