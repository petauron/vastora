package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestThreeXUIClientCommandsKeepLinksOneTimeAndMetadataSafe(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, created_at, updated_at) VALUES('three-x-ui-clients', '3x-ui', ?, ?, ?, '', 'running', 'docker', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
		VALUES('reality-service', 'three-x-ui-clients', ?, 'inbound-9', 'tcp', 35443, 35443, '10.0.0.80:35443', 'observed', 'vless/tcp/reality', 0, '10.0.0.80', 'ready', ?, ?),
		('subscription-service', 'three-x-ui-clients', ?, 'subscription', 'http', 2096, 2096, '10.0.0.80:2096', 'catalog', '', 0, '', 'ready', ?, ?)`, siteID, now, now, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, tls_enabled, status, created_at, updated_at)
		VALUES('reality-publication', 'reality-service', 'public_shared_443', ?, 'reality.example.test', 'www.example.com', 'manual', 0, 'ready', ?, ?),
		('subscription-publication', 'subscription-service', 'public_direct', ?, 'subscription.example.test', '', 'manual', 1, 'ready', ?, ?)`, node.ID, now, now, node.ID, now, now); err != nil {
		t.Fatal(err)
	}

	command, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.ClientCommand == nil || len(task.ClientCommand.Inbounds) != 1 || task.ClientCommand.Inbounds[0].ConnectHostname != "reality.example.test" || task.ClientCommand.SubscriptionBaseURI != "https://subscription.example.test/sub/" {
		t.Fatalf("unexpected client task: %#v", task)
	}
	metadata := []ThreeXUIClientView{{Email: "MacBook", Enabled: true, TotalBytes: 10 << 30, UsedBytes: 1024, InboundIDs: []int{9}, HasSubscription: true}}
	result, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || len(completed.Clients) != 1 || !completed.SubscriptionAvailable || completed.ResultAvailable {
		t.Fatalf("unexpected safe client result: %#v err=%v", completed, err)
	}

	reveal, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "reveal_link", Email: "MacBook", InboundID: 9})
	if err != nil {
		t.Fatal(err)
	}
	task = claimTask(t, store, node)
	secretLink := "vless://11111111-2222-4333-8444-555555555555@reality.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=www.example.com&pbk=public-key&sid=deadbeef#MacBook"
	result, _ = json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds, Secret: secretLink, SecretKind: "client_link"}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err = store.ApplicationCommand(ctx, reveal.ID)
	if err != nil || !completed.ResultAvailable {
		t.Fatalf("one-time client link was unavailable: %#v err=%v", completed, err)
	}
	var publicResult string
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(result_json AS TEXT) FROM application_commands WHERE id = ?`, reveal.ID).Scan(&publicResult); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(publicResult, "11111111-2222-4333-8444-555555555555") || strings.Contains(publicResult, "vless://") {
		t.Fatalf("client secret leaked into public command data: %s", publicResult)
	}
	consumed, err := store.ConsumeApplicationCommandResult(ctx, reveal.ID)
	if err != nil || consumed != secretLink {
		t.Fatalf("revealed link = %q err=%v", consumed, err)
	}
	if _, err := store.ConsumeApplicationCommandResult(ctx, reveal.ID); err == nil {
		t.Fatal("client link was revealed more than once")
	}

	clashReveal, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "reveal_clash_subscription", Email: "MacBook"})
	if err != nil {
		t.Fatal(err)
	}
	task = claimTask(t, store, node)
	clashLink := "https://subscription.example.test/clash/client-sub-id"
	result, _ = json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds, Secret: clashLink, SecretKind: "clash_subscription"}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err = store.ApplicationCommand(ctx, clashReveal.ID)
	if err != nil || !completed.ResultAvailable {
		t.Fatalf("one-time Clash subscription was unavailable: %#v err=%v", completed, err)
	}
	consumed, err = store.ConsumeApplicationCommandResult(ctx, clashReveal.ID)
	if err != nil || consumed != clashLink {
		t.Fatalf("revealed Clash subscription = %q err=%v", consumed, err)
	}
}
