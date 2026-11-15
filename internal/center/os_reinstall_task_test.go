package center

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"golang.org/x/crypto/ssh"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/recovery"
)

func reinstallAccessFixture(t *testing.T) (osreinstall.AccessSnapshot, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	pub := ssh.MarshalAuthorizedKey(signer.PublicKey())
	entries := map[string][]byte{"etc/ssh/ssh_host_ed25519_key": pem.EncodeToMemory(pemBlock), "etc/ssh/ssh_host_ed25519_key.pub": pub, "etc/ssh/sshd_config": []byte("PasswordAuthentication no\n")}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	tw := tar.NewWriter(z)
	for name, data := range entries {
		mode := int64(0644)
		if strings.HasSuffix(name, "_key") {
			mode = 0600
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return osreinstall.AccessSnapshot{Username: "debian", SSHPort: 22, SudoPolicy: "passwordless_all", SSHArchive: buf.Bytes(), AuthorizedKeys: pub, AgentUnit: []byte("[Service]\nExecStart=/usr/local/bin/vastora agent serve\n")}, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestOSReinstallRequiresFreshEvidenceAndDurableBackup(t *testing.T) {
	testOSReinstallReceipt(t, false)
}

func TestOSReinstallExpiredCompletionRetainsIdentityAndNetworkChecks(t *testing.T) {
	testOSReinstallReceipt(t, true)
}

func testOSReinstallReceipt(t *testing.T, expired bool) {
	t.Helper()
	ctx := context.Background()
	store := openOrchestrationStore(t)
	defer store.Close()
	if _, err := store.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('reinstall-admin','reinstall-admin','test-hash','')`); err != nil {
		t.Fatal(err)
	}
	candidates := []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}
	profile := networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}}
	caps := NodeCapabilities{Docker: true, HostProfile: true, OSReinstallPreflight: true, OSReinstall: true}
	node := enrollOrchestrationNode(t, store, "reinstall-lab", caps, candidates, profile)
	private, public, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE agents SET x25519_public_key=? WHERE id=?", public, node.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: Version, Roles: []string{"worker"}, Capabilities: caps, PublicKey: public, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, Startup: true}); err != nil {
		t.Fatal(err)
	}
	access, hostKey := reinstallAccessFixture(t)
	inspection := osreinstall.Inspection{
		Virtualization: "kvm", Architecture: "amd64", Hostname: "reinstall-lab", BootMode: "bios",
		BootID: "f0a3421b-7a2e-4c47-8f3b-948c3b63424a", OSID: "debian", OSVersion: "12",
		NetworkCandidate: "ifupdown_static_ipv4", SSHHostKeySHA256: hostKey,
		Limitations: []string{"network_configuration_requires_review"},
		Disk:        osreinstall.Disk{Path: "/dev/vda", Bytes: 8 << 30, MajorMinor: "252:0", PartitionTableID: "aabbccdd"},
		Network:     osreinstall.Network{Interface: "eth0", MAC: "02:00:00:00:00:01", Addresses: []osreinstall.Address{{Prefix: "192.0.2.10/24"}}, Gateways: []osreinstall.Gateway{{Family: "ipv4", Address: "192.0.2.1"}}, DNS: []string{"192.0.2.53"}},
	}
	host := nodediagnostics.HostProfile{CPUCount: 1, MemoryBytes: 1024, DiskBytes: 1024, Kernel: "test", Architecture: "amd64", OSReinstall: &inspection}
	result, _ := json.Marshal(nodediagnostics.Result{Host: &host})
	now := store.now().UTC().Format(time.RFC3339Nano)
	_, err = store.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,result_json,checked_at,created_at,updated_at,pair_key)
 VALUES(?,?,?,?,?,?,'succeeded',?,?,?,?,?)`, node.ID, nodediagnostics.HostProfileKind, "preflight-1", "", 1, osReinstallInspectionTargets, string(result), now, now, now, osReinstallInspectionPairKey)
	if err != nil {
		t.Fatal(err)
	}
	request := OSReinstallRequest{InspectionID: "preflight-1", ConfirmDisk: "/dev/vda", Username: "debian", SSHPort: 22, ConfirmErase: true}
	wrong := request
	wrong.ConfirmDisk = "/dev/vdb"
	if _, err = store.QueueOSReinstall(ctx, node.ID, "reinstall-admin", wrong); err == nil {
		t.Fatal("wrong disk accepted")
	}
	view, err := store.QueueOSReinstall(ctx, node.ID, "reinstall-admin", request)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "pending" {
		t.Fatal("operation not queued")
	}
	if _, err = store.QueueOSReinstall(ctx, node.ID, "reinstall-admin", request); err == nil {
		t.Fatal("duplicate operation accepted")
	}
	session := "reinstall-test-current-session-123456"
	if err = store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || task == nil || task.OSReinstall == nil || task.Kind != osreinstall.TaskKind {
		t.Fatalf("claim failed: %v", err)
	}
	if err = store.StartExecution(ctx, node.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	if err = store.CheckExecutionStep(ctx, node.ID, session, task.Authorization.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); err == nil {
		t.Fatal("second claim was not fenced")
	}

	dir := t.TempDir()
	agentStore, err := agent.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = agentStore.SaveConnection(ctx, agent.Connection{AgentID: node.ID, Name: "reinstall-lab", CenterURL: "http://127.0.0.1:8080", Credential: node.Credential, PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	if err = agentStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, agent.HostInstallStateName), []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=none\nTAILSCALE_ENROLLED=0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.enc")
	artifact, err := recovery.ExportAgent(ctx, dir, "", Version, path, task.OSReinstall.RecoveryPassword)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := osreinstall.StaticNetworkPlan{Interface: "eth0", MAC: "02:00:00:00:00:01", Address: "192.0.2.10/24", Gateway: "192.0.2.1", DNS: []string{"192.0.2.53"}, Resolver: []byte("nameserver 192.0.2.53\n")}
	accessCiphertext, err := osreinstall.SealAccessSnapshot(access, task.OSReinstall.RecoveryPassword, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	backup := osreinstall.BackupEvidence{Ciphertext: ciphertext, AccessCiphertext: accessCiphertext, Digest: artifact.Digest, Network: plan}
	if err = store.RecordOSReinstallBackup(ctx, node.ID, "wrong-session", task.Authorization.ID, backup); err == nil {
		t.Fatal("wrong session accepted")
	}
	bad := backup
	bad.Digest = "sha256:" + strings.Repeat("0", 64)
	if err = store.RecordOSReinstallBackup(ctx, node.ID, session, task.Authorization.ID, bad); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if err = store.RecordOSReinstallBackup(ctx, node.ID, session, task.Authorization.ID, backup); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordOSReinstallBackup(ctx, node.ID, session, task.Authorization.ID, backup); err == nil {
		t.Fatal("backup receipt replayed")
	}
	if err = store.CheckExecutionStep(ctx, node.ID, session, task.Authorization.ID, "apply"); err == nil {
		t.Fatal("reinstall phase replayed")
	}
	var state, digest string
	var stored []byte
	if err = store.db.QueryRow(`SELECT state,backup_digest,encrypted_backup FROM os_reinstall_tasks WHERE id=?`, view.ID).Scan(&state, &digest, &stored); err != nil {
		t.Fatal(err)
	}
	if state != "backup_ready" || digest != artifact.Digest || len(stored) != len(ciphertext) {
		t.Fatal("backup was not durably recorded")
	}
	if err = verifyCenterEncryptedState(ctx, store.db, store.key); err != nil {
		t.Fatal("reinstall backup key binding rejected", err)
	}
	var savedPlan []byte
	if err = store.db.QueryRow("SELECT sealed_network FROM os_reinstall_tasks WHERE id=?", view.ID).Scan(&savedPlan); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec("UPDATE os_reinstall_tasks SET sealed_network=X'01' WHERE id=?", view.ID); err != nil {
		t.Fatal(err)
	}
	if err = verifyCenterEncryptedState(ctx, store.db, store.key); err == nil {
		t.Fatal("corrupt reinstall network backup was accepted")
	}
	if _, err = store.db.Exec("UPDATE os_reinstall_tasks SET sealed_network=? WHERE id=?", savedPlan, view.ID); err != nil {
		t.Fatal(err)
	}
	arm := osreinstall.ArmEvidence{BackupDigest: artifact.Digest, PreparedDigest: "sha256:" + strings.Repeat("c", 64)}
	badArm := arm
	badArm.BackupDigest = "sha256:" + strings.Repeat("0", 64)
	if err = store.ArmOSReinstall(ctx, node.ID, session, task.Authorization.ID, badArm); err == nil {
		t.Fatal("wrong backup authorized")
	}
	if err = store.ArmOSReinstall(ctx, node.ID, session, task.Authorization.ID, arm); err != nil {
		t.Fatal(err)
	}
	if err = store.ArmOSReinstall(ctx, node.ID, session, task.Authorization.ID, arm); err == nil {
		t.Fatal("arm grant replayed")
	}
	if err = store.db.QueryRow("SELECT state FROM os_reinstall_tasks WHERE id=?", view.ID).Scan(&state); err != nil || state != "armed" {
		t.Fatal("arming was not durable")
	}
	if time.Since(artifact.Manifest.CreatedAt) > time.Minute {
		t.Fatal("test backup unexpectedly stale")
	}
	if expired {
		later := store.now().Add(time.Hour)
		store.now = func() time.Time { return later }
		if err = store.executionClaimAllowed(ctx, node.ID, session); err == nil {
			t.Fatal("expired install was allowed to claim more work")
		}
		var expiredState string
		if err = store.db.QueryRow("SELECT state FROM task_executions WHERE id=?", task.Authorization.ID).Scan(&expiredState); err != nil || expiredState != "unknown" {
			t.Fatal("expired execution evidence was not retained as unknown", err)
		}
		if err = store.ArmOSReinstall(ctx, node.ID, session, task.Authorization.ID, arm); err == nil {
			t.Fatal("expired execution obtained another boot grant")
		}
	}

	after := inspection
	after.BootID = "07e175ea-1e37-426c-9b50-22f366f06f88"
	after.OSVersion = "13"
	report := osreinstall.ReconnectReport{TaskID: view.ID, After: after, Network: plan}
	if err = store.CompleteOSReinstall(ctx, node.ID, node.Credential, session, task.Authorization.ID, report); err == nil {
		t.Fatal("original session accepted as a new boot")
	}
	newSession := "reinstall-test-new-session-987654321"
	if err = store.RegisterExecutionSession(ctx, node.ID, node.Credential, newSession, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	badReport := report
	badReport.After.BootID = inspection.BootID
	if err = store.CompleteOSReinstall(ctx, node.ID, node.Credential, newSession, task.Authorization.ID, badReport); err == nil {
		t.Fatal("same boot accepted")
	}
	badReport = report
	badReport.Network.Resolver = []byte("nameserver 192.0.2.54\n")
	if err = store.CompleteOSReinstall(ctx, node.ID, node.Credential, newSession, task.Authorization.ID, badReport); err == nil {
		t.Fatal("changed resolver accepted")
	}
	if err = store.CompleteOSReinstall(ctx, node.ID, "wrong credential", newSession, task.Authorization.ID, report); err == nil {
		t.Fatal("wrong credential accepted")
	}
	if err = store.CompleteOSReinstall(ctx, node.ID, node.Credential, newSession, task.Authorization.ID, report); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT state FROM os_reinstall_tasks WHERE id=?", view.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("reconnect not recorded")
	}
	if err = store.CompleteOSReinstall(ctx, node.ID, node.Credential, newSession, task.Authorization.ID, report); err != nil {
		t.Fatal("idempotent reconnect rejected", err)
	}
}
