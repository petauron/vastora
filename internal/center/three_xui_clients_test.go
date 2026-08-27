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
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	workerNode := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.81", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.81", LANAddress: "10.0.0.81", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES('three-x-ui-clients', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
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
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id, inbound_tag, total_bytes, reset_days, next_reset_at, revision, status, updated_at)
		VALUES('reality-service', 'vastora-node-9', 0, 0, '', 1, 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('three-x-ui-worker', 'Worker', ?, ?, ?, '', 'running', 'docker', 'worker', ?, ?)`, workerNode.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('worker-reality-pending', 'three-x-ui-worker', ?, ?, '3xui.reality.create', '{malformed', 'pending', ?, ?)`, node.ID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "list_inbounds"}); err == nil || !strings.Contains(err.Error(), "operation in progress") {
		t.Fatalf("client command did not serialize with Worker REALITY creation: %v", err)
	}
	discarded, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || discarded != nil {
		t.Fatalf("malformed stored command claim=%#v err=%v", discarded, err)
	}
	var discardedState, discardedError string
	if err := store.db.QueryRowContext(ctx, `SELECT state, error FROM application_commands WHERE id = 'worker-reality-pending'`).Scan(&discardedState, &discardedError); err != nil {
		t.Fatal(err)
	}
	if discardedState != "failed" || !strings.Contains(discardedError, "stored REALITY operation is invalid") {
		t.Fatalf("malformed stored command state=%q error=%q", discardedState, discardedError)
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
	result, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds, InboundsObserved: true}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || len(completed.Clients) != 1 || !completed.SubscriptionAvailable || completed.ResultAvailable {
		t.Fatalf("unexpected safe client result: %#v err=%v", completed, err)
	}

	inboundList, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "list_inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	task = claimTask(t, store, node)
	result, _ = json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Inbounds: task.ClientCommand.Inbounds, InboundsObserved: true}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err = store.ApplicationCommand(ctx, inboundList.ID)
	if err != nil || completed.State != "succeeded" || !completed.InboundsObserved || completed.ClientsObserved || len(completed.Clients) != 0 {
		t.Fatalf("unexpected inbound-only result: %#v err=%v", completed, err)
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

	subscriptionReveal, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-clients", Action: "reveal_subscription", Email: "MacBook"})
	if err != nil {
		t.Fatal(err)
	}
	task = claimTask(t, store, node)
	subscriptionLink := "https://subscription.example.test/sub/client-sub-id"
	result, _ = json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds, Secret: subscriptionLink, SecretKind: "subscription"}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err = store.ApplicationCommand(ctx, subscriptionReveal.ID)
	if err != nil || !completed.ResultAvailable {
		t.Fatalf("one-time subscription was unavailable: %#v err=%v", completed, err)
	}
	consumed, err = store.ConsumeApplicationCommandResult(ctx, subscriptionReveal.ID)
	if err != nil || consumed != subscriptionLink {
		t.Fatalf("revealed subscription = %q err=%v", consumed, err)
	}
}

func TestThreeXUIClientCommandSelectsMultipleSiteNodes(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES('three-x-ui-controller', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
		VALUES('reality-9', 'three-x-ui-controller', ?, 'inbound-9', 'tcp', 30009, 30009, '10.0.0.80:30009', 'observed', 'vless/tcp/reality', 0, '10.0.0.80', 'ready', ?, ?),
		('reality-10', 'three-x-ui-controller', ?, 'inbound-10', 'tcp', 30010, 30010, '10.0.0.80:30010', 'observed', 'vless/tcp/reality', 0, '10.0.0.80', 'ready', ?, ?)`, siteID, now, now, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id, inbound_tag, total_bytes, reset_days, next_reset_at, revision, status, updated_at)
		VALUES('reality-9', 'vastora-node-9', 0, 0, '', 1, 'active', ?),
		('reality-10', 'vastora-node-10', 0, 0, '', 1, 'active', ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	command, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-controller", Action: "create", NewEmail: "Router", InboundIDs: []int{10, 9, 10}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.ClientCommand == nil || len(task.ClientCommand.InboundIDs) != 2 || task.ClientCommand.InboundIDs[0] != 9 || task.ClientCommand.InboundIDs[1] != 10 {
		t.Fatalf("multi-node client task = %#v", task.ClientCommand)
	}
	metadata := []ThreeXUIClientView{{Email: "Router", Enabled: true, InboundIDs: []int{9, 10}, HasSubscription: true}}
	result, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Clients: metadata, ClientsObserved: true, Inbounds: task.ClientCommand.Inbounds}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || len(completed.Clients) != 1 || len(completed.Clients[0].InboundIDs) != 2 {
		t.Fatalf("multi-node client result = %#v err=%v", completed, err)
	}
}
