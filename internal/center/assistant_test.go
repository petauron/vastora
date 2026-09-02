package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestAssistantProviderIsWriteOnlyAndURLPolicyFailsClosed(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	secretValue := "provider-secret-that-must-not-leak"
	store.assistantResolve = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	}
	view, err := store.SaveAssistantProvider(ctx, AssistantProviderInput{APIURL: "https://provider.example/v1/", APIKey: secretValue, Model: "safe-model"})
	if err != nil {
		t.Fatal(err)
	}
	if !view.APIKeySet || view.APIURL != "https://provider.example/v1" {
		t.Fatalf("unexpected provider view: %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil || bytes.Contains(encoded, []byte(secretValue)) || bytes.Contains(encoded, []byte(`"apiKey":`)) {
		t.Fatalf("provider response exposed its key: %s, err=%v", encoded, err)
	}
	var sealed []byte
	if err := store.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = (SELECT api_key_secret_id FROM assistant_model_providers WHERE id = 1)`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secretValue)) {
		t.Fatal("provider API key was stored in plaintext")
	}
	credentials, err := store.assistantProviderCredentials(ctx)
	if err != nil || credentials.APIKey != secretValue {
		t.Fatalf("encrypted provider key could not be recovered: key=%q err=%v", credentials.APIKey, err)
	}

	resolve := func(ip string) func(context.Context, string) ([]net.IPAddr, error) {
		return func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil }
	}
	for name, test := range map[string]struct {
		url          string
		allowPrivate bool
		resolve      func(context.Context, string) ([]net.IPAddr, error)
	}{
		"loopback":            {url: "https://localhost/v1", resolve: resolve("127.0.0.1")},
		"private":             {url: "https://model.internal/v1", resolve: resolve("10.0.0.8")},
		"metadata link local": {url: "http://metadata/v1", allowPrivate: true, resolve: resolve("169.254.169.254")},
		"metadata private":    {url: "https://metadata/v1", allowPrivate: true, resolve: resolve("100.100.100.200")},
		"metadata ipv6":       {url: "https://metadata/v1", allowPrivate: true, resolve: resolve("fd00:ec2::254")},
		"public plaintext":    {url: "http://provider.example/v1", allowPrivate: true, resolve: resolve("203.0.113.10")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeAssistantAPIURL(ctx, test.url, test.allowPrivate, test.resolve); err == nil {
				t.Fatalf("unsafe provider URL was accepted: %s", test.url)
			}
		})
	}
	if normalized, err := normalizeAssistantAPIURL(ctx, "http://model.internal:8080/v1/", true, resolve("10.0.0.8")); err != nil || normalized != "http://model.internal:8080/v1" {
		t.Fatalf("explicit private provider was rejected: normalized=%q err=%v", normalized, err)
	}

	store.assistantResolve = resolve("10.0.0.8")
	if _, err := store.ValidateAssistantProvider(ctx); err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("DNS rebinding to a private address was not rejected: %v", err)
	}
}

func TestAssistantProviderRejectsRedirectsAndHonorsCancellation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := store.SaveAssistantProvider(ctx, AssistantProviderInput{APIURL: redirect.URL, APIKey: "redirect-key", Model: "model", AllowPrivate: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateAssistantProvider(ctx); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("provider redirect was not rejected: %v", err)
	}
	if redirected.Load() != 0 {
		t.Fatal("provider validation followed an unsafe redirect")
	}

	slow := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		writer.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer slow.Close()
	if _, err := store.SaveAssistantProvider(ctx, AssistantProviderInput{APIURL: slow.URL, Model: "model", AllowPrivate: true}); err != nil {
		t.Fatal(err)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := store.ValidateAssistantProvider(timeoutCtx); err == nil {
		t.Fatal("provider validation ignored request cancellation")
	}
}

func TestAssistantProposalRequiresExactApprovalAndAppliesExactlyOnce(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "assistant-admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "assistant-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	conversation, err := store.CreateAssistantConversation(ctx, adminID, "Install 3x-ui")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.QueueAssistantMessage(ctx, adminID, conversation.ID, "请安装 3x-ui")
	if err != nil {
		t.Fatal(err)
	}
	request := assistantInstallRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)}
	preview, err := store.PreviewAssistantInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateAssistantProposal(ctx, adminID, conversation.ID, run.ID, preview)
	if err != nil {
		t.Fatal(err)
	}
	var deployments int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil || deployments != 0 {
		t.Fatalf("proposal created work before approval: deployments=%d err=%v", deployments, err)
	}
	if _, err := store.DecideAssistantProposal(ctx, "another-admin", proposal.ID, "approved", proposal.Digest); err == nil {
		t.Fatal("proposal accepted an unauthorized approver")
	}
	if _, err := store.DecideAssistantProposal(ctx, adminID, proposal.ID, "approved", "wrong-digest"); err == nil {
		t.Fatal("proposal accepted the wrong digest")
	}
	if _, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest); err == nil {
		t.Fatal("pending proposal executed without trusted approval")
	}
	approved, err := store.DecideAssistantProposal(ctx, adminID, proposal.ID, "approved", proposal.Digest)
	if err != nil || approved.Status != "approved" {
		t.Fatalf("exact proposal approval failed: %#v err=%v", approved, err)
	}
	first, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest)
	if err != nil || second.ID != first.ID {
		t.Fatalf("proposal replay created a different deployment: first=%q second=%q err=%v", first.ID, second.ID, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE change_proposal_id = ?`, proposal.ID).Scan(&deployments); err != nil || deployments != 1 {
		t.Fatalf("approved proposal deployment count=%d err=%v", deployments, err)
	}
	task := claimTask(t, store, node)
	if task.ID != first.ID || task.Kind != "application.apply" {
		t.Fatalf("approved proposal queued the wrong Agent task: %#v", task)
	}
	if duplicate, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || duplicate != nil {
		t.Fatalf("approved proposal queued more than one Agent task: task=%#v err=%v", duplicate, err)
	}
	var auditKinds int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT kind) FROM assistant_audit_events WHERE proposal_id = ?`, proposal.ID).Scan(&auditKinds); err != nil || auditKinds < 3 {
		t.Fatalf("proposal audit chain is incomplete: kinds=%d err=%v", auditKinds, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'approval_required' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false)
	server.watchAssistantDeployment(proposal, first, adminID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		events, err := store.AssistantEvents(ctx, adminID, conversation.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		seenRunning := false
		for _, event := range events {
			seenRunning = seenRunning || event.Event == "execution.running"
		}
		if seenRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assistant event stream did not report the running deployment")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// No prefix or sensitive field name: diagnostic isolation must not depend
	// on recognizing this synthetic short credential.
	runtimeError := "upstream rejected the supplied value: horse42"
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'failed', error = ?, updated_at = ? WHERE id = ?`, runtimeError, time.Now().UTC().Format(time.RFC3339Nano), first.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		var status, lastError string
		if err := store.db.QueryRowContext(ctx, `SELECT status, last_error FROM assistant_runs WHERE id = ?`, run.ID).Scan(&status, &lastError); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			if strings.Contains(lastError, "horse42") || lastError != assistantDiagnosticSummary(runtimeError) {
				t.Fatalf("Agent failure was not isolated: %q", lastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Agent failure was not reported back to the assistant run: %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	events, err := store.AssistantEvents(ctx, adminID, conversation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"execution.failed": false, "run.failed": false}
	for _, event := range events {
		if _, ok := wanted[event.Event]; ok {
			wanted[event.Event] = true
		}
		if bytes.Contains(event.Data, []byte("horse42")) {
			t.Fatalf("SSE event leaked Agent failure credentials: %s", event.Data)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("assistant event stream did not include %s", name)
		}
	}
	toolResult, _, err := server.executeAssistantTool(ctx, adminID, run, "explain-tool", "explain_failure", assistantJSON(map[string]string{"deploymentId": first.ID}), assistantPreviewCache{})
	if err != nil || bytes.Contains(toolResult, []byte("horse42")) || !bytes.Contains(toolResult, []byte("internal_error")) {
		t.Fatalf("failure explanation did not isolate runtime details: %s err=%v", toolResult, err)
	}
	history, err := store.assistantMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history {
		if strings.Contains(message.Content, "horse42") {
			t.Fatal("raw runtime diagnostic entered assistant history")
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'approval_required', last_error = '' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	NewServer(store, "", false).resumeAssistantExecutions()
	deadline = time.Now().Add(3 * time.Second)
	for {
		var status string
		if err := store.db.QueryRowContext(ctx, `SELECT status FROM assistant_runs WHERE id = ?`, run.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal assistant execution did not recover after restart: %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'succeeded', error = '', updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'approval_required', last_error = '' WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	NewServer(store, "", false).resumeAssistantExecutions()
	deadline = time.Now().Add(3 * time.Second)
	for {
		var status string
		if err := store.db.QueryRowContext(ctx, `SELECT status FROM assistant_runs WHERE id = ?`, run.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("successful Agent execution was not recovered into the assistant run: %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	events, err = store.AssistantEvents(ctx, adminID, conversation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wanted = map[string]bool{"execution.completed": false, "run.completed": false}
	for _, event := range events {
		if _, ok := wanted[event.Event]; ok {
			wanted[event.Event] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("assistant event stream did not include %s", name)
		}
	}
}

func TestAssistantCPACredentialRotationRequiresApprovalAndKeepsSecretsOutOfModelData(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "assistant-admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "assistant-cpa-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})
	install, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, node)
	var original cpaCredentialValues
	if err := json.Unmarshal(installTask.Secrets, &original); err != nil || original.ManagementKey == "" || original.APIKey == "" {
		t.Fatalf("CPA install did not generate credentials: %#v err=%v", original, err)
	}
	completeApplicationTaskForCredentialTest(t, store, node, installTask, "10.0.0.93", 8317)

	conversation, err := store.CreateAssistantConversation(ctx, adminID, "Rotate CPA client key")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.QueueAssistantMessage(ctx, adminID, conversation.ID, "轮换 CPA 客户端密钥")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false)
	previews := assistantPreviewCache{installations: map[string]assistantInstallPreview{}, rotations: map[string]assistantCredentialRotationPreview{}}
	previewResult, _, err := server.executeAssistantTool(ctx, adminID, run, "preview-tool", "preview_rotate_cpa_credential", assistantJSON(map[string]string{"applicationId": install.ApplicationID, "target": "client"}), previews)
	if err != nil {
		t.Fatal(err)
	}
	var preview assistantCredentialRotationPreview
	if err := json.Unmarshal(previewResult, &preview); err != nil {
		t.Fatal(err)
	}
	proposalResult, proposal, err := server.executeAssistantTool(ctx, adminID, run, "proposal-tool", "propose_change", assistantJSON(map[string]string{"previewDigest": preview.Digest}), previews)
	if err != nil || proposal == nil || proposal.Kind != "rotate_cpa_credential" || proposal.Risk != "high" {
		t.Fatalf("assistant CPA proposal = %#v err=%v", proposal, err)
	}
	for _, secretValue := range []string{original.ManagementKey, original.APIKey} {
		if bytes.Contains(previewResult, []byte(secretValue)) || bytes.Contains(proposalResult, []byte(secretValue)) {
			t.Fatal("assistant CPA preview or proposal exposed an existing credential")
		}
	}
	var rotations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_credential_rotations`).Scan(&rotations); err != nil || rotations != 0 {
		t.Fatalf("assistant CPA proposal created work before approval: count=%d err=%v", rotations, err)
	}
	if _, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest); err == nil {
		t.Fatal("pending assistant CPA proposal executed without approval")
	}
	if _, err := store.DecideAssistantProposal(ctx, adminID, proposal.ID, "approved", proposal.Digest); err != nil {
		t.Fatal(err)
	}
	execution, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest)
	if err != nil || execution.Kind != "rotate_cpa_credential" || execution.ID == "" {
		t.Fatalf("assistant CPA execution = %#v err=%v", execution, err)
	}
	replayed, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest)
	if err != nil || replayed.ID != execution.ID {
		t.Fatalf("assistant CPA replay = %#v err=%v", replayed, err)
	}
	rotationTask := claimTask(t, store, node)
	var rotated cpaCredentialValues
	if err := json.Unmarshal(rotationTask.Secrets, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.ManagementKey != original.ManagementKey || rotated.APIKey == "" || rotated.APIKey == original.APIKey {
		t.Fatalf("assistant rotated the wrong CPA credential: before=%#v after=%#v", original, rotated)
	}
	completeApplicationTaskForCredentialTest(t, store, node, rotationTask, "10.0.0.93", 8317)
	completed, err := store.refreshCredentialRotation(ctx, execution.ID)
	if err != nil || completed.State != "succeeded" {
		t.Fatalf("assistant CPA rotation completion = %#v err=%v", completed, err)
	}

	var proposalData, auditData []byte
	if err := store.db.QueryRowContext(ctx, `SELECT request_json || summary_json || targets_json FROM change_proposals WHERE id = ?`, proposal.ID).Scan(&proposalData); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(group_concat(payload_json, ''), '') FROM assistant_audit_events WHERE proposal_id = ?`, proposal.ID).Scan(&auditData); err != nil {
		t.Fatal(err)
	}
	for _, secretValue := range []string{original.ManagementKey, original.APIKey, rotated.APIKey} {
		if bytes.Contains(proposalData, []byte(secretValue)) || bytes.Contains(auditData, []byte(secretValue)) {
			t.Fatal("assistant proposal or audit data exposed a CPA credential")
		}
	}
}

func TestAssistantRejectionStaleRevisionAndToolBoundaryFailClosed(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "assistant-admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, _ := store.SessionAdminID(ctx, session)
	node := enrollOrchestrationNode(t, store, "rejected-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	conversation, _ := store.CreateAssistantConversation(ctx, adminID, "Approval boundaries")
	run, _ := store.QueueAssistantMessage(ctx, adminID, conversation.ID, "yes, confirm and install")
	request := assistantInstallRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{}`)}
	preview, err := store.PreviewAssistantInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.CreateAssistantProposal(ctx, adminID, conversation.ID, run.ID, preview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideAssistantProposal(ctx, adminID, proposal.ID, "rejected", proposal.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyAssistantProposal(ctx, adminID, proposal.ID, proposal.Digest); err == nil {
		t.Fatal("rejected proposal was executable")
	}
	previews := assistantPreviewCache{installations: map[string]assistantInstallPreview{}, rotations: map[string]assistantCredentialRotationPreview{}}
	if _, _, err := NewServer(store, "", false).executeAssistantTool(ctx, adminID, run, "tool", "shell", json.RawMessage(`{"command":"id"}`), previews); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("excluded high-risk tool was accepted: %v", err)
	}
	if !assistantArgumentsContainSensitiveData(json.RawMessage(`{"config":{"api_key":"must-not-persist"}}`)) {
		t.Fatal("sensitive model tool arguments were not rejected")
	}
	nodeResult, _, err := NewServer(store, "", false).executeAssistantTool(ctx, adminID, run, "tool", "list_nodes", json.RawMessage(`{}`), previews)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(nodeResult, []byte("10.0.0.92")) || bytes.Contains(nodeResult, []byte("networkCandidates")) {
		t.Fatalf("sanitized node tool exposed network details: %s", nodeResult)
	}
	actions := sanitizeAssistantActions([]ActionView{{ID: "event", Message: "password=must-not-leak"}})
	actionJSON, _ := json.Marshal(actions)
	if bytes.Contains(actionJSON, []byte("must-not-leak")) {
		t.Fatalf("sanitized action tool exposed credentials: %s", actionJSON)
	}
	input := "install with sk-abcdefghijklmnopqrstuvwxyz012345 and -----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----"
	redacted := redactAssistantText(input)
	if strings.Contains(redacted, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(redacted, "private-material") {
		t.Fatalf("assistant input redaction missed a common credential: %q", redacted)
	}

	secondRun, _ := store.QueueAssistantMessage(ctx, adminID, conversation.ID, "create another preview")
	secondPreview, err := store.PreviewAssistantInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.CreateAssistantProposal(ctx, adminID, conversation.ID, secondRun.ID, secondPreview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideAssistantProposal(ctx, adminID, stale.ID, "approved", stale.Digest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale resource revision was approved: %v", err)
	}
	var approvals int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_approvals WHERE proposal_id = ?`, stale.ID).Scan(&approvals); err != nil || approvals != 0 {
		t.Fatalf("stale proposal left an approval: count=%d err=%v", approvals, err)
	}

	expiredRun, _ := store.QueueAssistantMessage(ctx, adminID, conversation.ID, "create an expiring preview")
	expiredPreview, err := store.PreviewAssistantInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.CreateAssistantProposal(ctx, adminID, conversation.ID, expiredRun.ID, expiredPreview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE change_proposals SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideAssistantProposal(ctx, adminID, expired.ID, "approved", expired.Digest); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired proposal was approved: %v", err)
	}
}

func TestAssistantRejectsPotentialCredentialsBeforePersistence(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	session, _, err := store.CreateFirstAdmin(ctx, "assistant-admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := store.CreateAssistantConversation(ctx, adminID, "Credential boundary")
	if err != nil {
		t.Fatal(err)
	}

	inputs := []string{
		"请检查这个未知格式值 V7mQ2fL9sN4xK8cR1pT6wY3hJ5uB0dEza",
		"客户端密钥是 aZ7mQ2fL9sN4xK8cR1pT6wY3hJ5uB0dEz",
		"Authorization: Bearer opaque-value-that-must-not-persist",
	}
	for _, input := range inputs {
		if _, err := store.CreateAssistantConversation(ctx, adminID, input); err == nil {
			t.Fatal("credential-like conversation title was accepted")
		}
		if _, err := store.QueueAssistantMessage(ctx, adminID, conversation.ID, input); err == nil || !strings.Contains(err.Error(), "appears to contain a credential") {
			t.Fatalf("credential-like input was accepted: input=%q err=%v", input, err)
		}
	}
	var messages, runs int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_messages WHERE conversation_id = ?`, conversation.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assistant_runs WHERE conversation_id = ?`, conversation.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || runs != 0 {
		t.Fatalf("rejected credential input was persisted: messages=%d runs=%d", messages, runs)
	}

	for _, safe := range []string{
		"检查提交 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"检查节点 7a0a3214-4076-4ae4-9b77-96acbb143d42",
		"检查域名 c7wcnfto2bz6zhucdqdxz7zkly.775811.xyz",
	} {
		if assistantTextContainsPotentialCredential(safe) {
			t.Fatalf("safe identifier was treated as a credential: %q", safe)
		}
	}
}

func TestAssistantToolArgumentsInspectEveryDecodedString(t *testing.T) {
	for _, input := range []string{
		`{"config":{"notes":"sk-abcdefghijklmnopqrstuvwxyz012345"}}`,
		`{"config":{"notes":["safe",{"value":"V7mQ2fL9sN4xK8cR1pT6wY3hJ5uB0dEza"}]}}`,
		`{"config":{"api-key":"horse42"}}`,
		`{"config":{"密钥":"horse42"}}`,
		`{"config":{"notes":"\u0073\u006b-abcdefghijklmnopqrstuvwxyz012345","notes":"safe"}}`,
		`{"config":{"notes":"safe","notes":"\u0073\u006b-abcdefghijklmnopqrstuvwxyz012345"}}`,
		`{"config":{"authori\u007aation":"horse42"}}`,
		`{"config":`,
	} {
		if !assistantArgumentsContainSensitiveData(json.RawMessage(input)) {
			t.Errorf("sensitive or invalid tool arguments accepted: %s", input)
		}
	}
	for _, input := range []string{
		`{}`, `{"config":{"enabled":true,"port":443,"optional":null}}`,
		`{"agentId":"7a0a3214-4076-4ae4-9b77-96acbb143d42","config":{"names":["first","second"]}}`,
	} {
		if assistantArgumentsContainSensitiveData(json.RawMessage(input)) {
			t.Errorf("safe tool arguments rejected: %s", input)
		}
	}
}

func TestAssistantRuntimeDiagnosticsAreStructurallyExcluded(t *testing.T) {
	for _, value := range []string{"horse42", "0123456789abcdef0123456789abcdef", "7a0a3214-4076-4ae4-9b77-96acbb143d42"} {
		payload := assistantJSON(map[string]any{
			"error":        assistantDiagnosticSummary("upstream rejected " + value),
			"actions":      sanitizeAssistantActions([]ActionView{{ID: "action", Message: value}}),
			"applications": sanitizeAssistantApplications([]ApplicationView{{ID: "app", NodeSyncError: value}}),
		})
		if bytes.Contains(payload, []byte(value)) {
			t.Fatal("raw diagnostic reached the assistant data projection")
		}
	}
	if assistantDiagnosticSummary("") != "" {
		t.Fatal("an empty diagnostic became an error")
	}
}
