package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/center"
)

// Run the actual CLI entry points in separate processes, without installing
// systemd units or sharing Agent databases. No test server fabricates heartbeats.
func TestNodeLifecycleProcessHelper(t *testing.T) {
	if os.Getenv("VASTORA_NODE_LIFECYCLE_CHILD") != "1" {
		t.Skip("subprocess entry point")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	t.Fatal("missing child command")
}

func TestCenterTwoAgentsLifecycle(t *testing.T) {
	if os.Getenv("VASTORA_NODE_LIFECYCLE_INTEGRATION") != "1" {
		t.Skip("enabled by the dedicated CI integration step")
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Fatal("requires a non-root Linux user; never install Agents on the runner host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	lab := &nodeLifecycleLab{t: t, ctx: ctx, root: t.TempDir()}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	lab.client = &http.Client{Jar: jar, Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(lab.client.CloseIdleConnections)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	lab.origin = "http://" + address
	centerArgs := []string{"center", "serve", "--data-dir", filepath.Join(lab.root, "center"), "--listen", address, "--agent-connect-url", lab.origin}
	controller := lab.start("center", centerArgs...)
	lab.ready(controller)
	lab.call("POST", "/api/v1/setup/admin", map[string]string{"username": "test-admin", "password": lab.secret()}, nil, http.StatusCreated)
	var setup struct {
		Site struct {
			ID string `json:"id"`
		} `json:"site"`
	}
	lab.call("POST", "/api/v1/setup/complete", map[string]any{
		"site":    map[string]string{"name": "CI lifecycle", "code": "ci-lifecycle", "timezone": "UTC"},
		"network": map[string]string{"agentConnectionMode": "lan", "agentConnectUrl": lab.origin},
	}, &setup, http.StatusCreated)
	target := lab.enroll("target", setup.Site.ID)
	peer := lab.enroll("peer", setup.Site.ID)
	targetProcess := lab.serve(target)
	peerProcess := lab.serve(peer)
	lab.wait("both Agent heartbeats", func() bool { return lab.connected(target.AgentID) && lab.connected(peer.AgentID) })
	peerSeen := lab.node(peer.AgentID).LastSeenAt

	t.Log("online: refuse permanent removal, then disable and delete through management APIs")
	targetPath := "/api/v1/agents/" + target.AgentID
	lab.call("POST", targetPath+"/remove", map[string]string{"confirmation": target.Name}, nil, http.StatusBadRequest)
	lab.call("POST", targetPath+"/disable", map[string]any{}, nil, http.StatusOK)
	lab.call("DELETE", targetPath, nil, nil, http.StatusOK)
	lab.assertRevoked(target)
	lab.assertPeer(peer, peerProcess, peerSeen)
	targetProcess.stop()
	targetProcess = lab.serve(target)
	lab.assertRestartRejected(target, targetProcess)
	targetProcess.stop()

	t.Log("offline: stop a real Agent, wait for heartbeat expiry, remove, and restart Center")
	offline := lab.enroll("offline-target", setup.Site.ID)
	offlineProcess := lab.serve(offline)
	lab.wait("new Agent heartbeat", func() bool { return lab.connected(offline.AgentID) })
	offlineProcess.stop()
	lastSeen := lab.node(offline.AgentID).LastSeenAt
	lab.wait("natural offline detection", func() bool { return !lab.connected(offline.AgentID) })
	if !lab.node(offline.AgentID).LastSeenAt.Equal(lastSeen) {
		t.Fatal("stopped Agent sent another heartbeat")
	}
	offlinePath := "/api/v1/agents/" + offline.AgentID + "/remove"
	lab.call("POST", offlinePath, map[string]string{"confirmation": "wrong-name"}, nil, http.StatusBadRequest)
	lab.call("POST", offlinePath, map[string]string{"confirmation": offline.Name}, nil, http.StatusAccepted)
	// Restart immediately after durable intent. Cleanup may already have finished;
	// either way the final state must survive a fresh Center process.
	controller.stop()
	controller = lab.start("center-restarted", centerArgs...)
	lab.ready(controller)
	lab.wait("persisted offline removal completes", func() bool { return lab.node(offline.AgentID) == nil })
	lab.assertRevoked(offline)
	peerSeen = lab.node(peer.AgentID).LastSeenAt
	lab.assertPeer(peer, peerProcess, peerSeen)
	offlineProcess = lab.serve(offline)
	lab.assertRestartRejected(offline, offlineProcess)
	lab.assertPeer(peer, peerProcess, peerSeen)
	var apps struct {
		Applications []center.ApplicationView `json:"applications"`
	}
	lab.call("GET", "/api/v1/applications", nil, &apps, http.StatusOK)
	if len(apps.Applications) != 0 {
		t.Fatal("clean-node lifecycle unexpectedly created application records")
	}
}

type nodeLifecycleProcess struct {
	logPath string
	cmd     *exec.Cmd
	done    chan error
	stopped bool
}

func (p *nodeLifecycleProcess) stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.cmd.Process.Kill()
	<-p.done
}
func (p *nodeLifecycleProcess) alive(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.done:
		p.stopped = true
		t.Fatalf("CLI process exited unexpectedly: %v", err)
	default:
	}
}

type nodeLifecycleAgent struct {
	agent.Connection
	directory string
}
type nodeLifecycleLab struct {
	t            *testing.T
	ctx          context.Context
	root, origin string
	client       *http.Client
	secrets      []string
}

func (l *nodeLifecycleLab) secret() string {
	l.t.Helper()
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		l.t.Fatal(err)
	}
	value := hex.EncodeToString(raw)
	l.secrets = append(l.secrets, value)
	return value
}
func (l *nodeLifecycleLab) command(args ...string) *exec.Cmd {
	l.t.Helper()
	binary, err := os.Executable()
	if err != nil {
		l.t.Fatal(err)
	}
	command := exec.CommandContext(l.ctx, binary, append([]string{"-test.run=^TestNodeLifecycleProcessHelper$", "--"}, args...)...)
	command.Env = append(os.Environ(), "VASTORA_NODE_LIFECYCLE_CHILD=1")
	return command
}
func (l *nodeLifecycleLab) start(label string, args ...string) *nodeLifecycleProcess {
	l.t.Helper()
	log, err := os.CreateTemp(l.root, label+"-*.log")
	if err != nil {
		l.t.Fatal(err)
	}
	cmd := l.command(args...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		l.t.Fatal(err)
	}
	p := &nodeLifecycleProcess{cmd: cmd, done: make(chan error, 1), logPath: log.Name()}
	go func() { p.done <- cmd.Wait() }()
	l.t.Cleanup(func() {
		p.stop()
		log.Close()
		if l.t.Failed() {
			raw, _ := os.ReadFile(log.Name())
			if len(raw) > 4096 {
				raw = raw[len(raw)-4096:]
			}
			text := string(raw)
			for _, secret := range l.secrets {
				text = strings.ReplaceAll(text, secret, "[redacted]")
			}
			l.t.Logf("%s log: %s", label, text)
		}
	})
	return p
}
func (l *nodeLifecycleLab) wait(label string, ready func() bool) {
	l.t.Helper()
	for !ready() {
		select {
		case <-l.ctx.Done():
			l.t.Fatalf("timed out: %s", label)
		case <-time.After(time.Second):
		}
	}
}
func (l *nodeLifecycleLab) ready(p *nodeLifecycleProcess) {
	l.t.Helper()
	l.wait("Center health", func() bool {
		p.alive(l.t)
		response, err := l.client.Get(l.origin + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
}
func (l *nodeLifecycleLab) call(method, path string, input, output any, status int) {
	l.t.Helper()
	var data []byte
	if input != nil {
		var err error
		data, err = json.Marshal(input)
		if err != nil {
			l.t.Fatal(err)
		}
	}
	request, err := http.NewRequestWithContext(l.ctx, method, l.origin+path, bytes.NewReader(data))
	if err != nil {
		l.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", l.origin)
	origin, _ := url.Parse(l.origin)
	for _, cookie := range l.client.Jar.Cookies(origin) {
		if cookie.Name == "vastora_csrf" {
			request.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}
	response, err := l.client.Do(request)
	if err != nil {
		l.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		l.t.Fatalf("%s %s: status %d, want %d", method, path, response.StatusCode, status)
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			l.t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
}
func (l *nodeLifecycleLab) node(id string) *center.AgentView {
	l.t.Helper()
	var result struct {
		Agents []center.AgentView `json:"agents"`
	}
	l.call("GET", "/api/v1/agents", nil, &result, http.StatusOK)
	for i := range result.Agents {
		if result.Agents[i].ID == id {
			return &result.Agents[i]
		}
	}
	return nil
}
func (l *nodeLifecycleLab) connected(id string) bool {
	node := l.node(id)
	return node != nil && node.Connected && node.LastSeenAt.After(node.EnrolledAt)
}
func (l *nodeLifecycleLab) enroll(name, siteID string) nodeLifecycleAgent {
	l.t.Helper()
	var enrollment struct {
		Token string `json:"token"`
	}
	l.call("POST", "/api/v1/agent-enrollments", map[string]any{"name": name, "siteId": siteID, "centerUrl": l.origin}, &enrollment, http.StatusCreated)
	if enrollment.Token == "" {
		l.t.Fatal("missing enrollment token")
	}
	l.secrets = append(l.secrets, enrollment.Token)
	tokenFile := filepath.Join(l.root, name+".token")
	if err := os.WriteFile(tokenFile, []byte(enrollment.Token), 0600); err != nil {
		l.t.Fatal(err)
	}
	directory := filepath.Join(l.root, name)
	if err := l.command("agent", "enroll", "--data-dir", directory, "--center-url", l.origin, "--token-file", tokenFile).Run(); err != nil {
		l.t.Fatalf("enroll %s: %v", name, err)
	}
	_ = os.Remove(tokenFile)
	store, err := agent.Open(directory)
	if err != nil {
		l.t.Fatal(err)
	}
	connection, err := store.Connection(l.ctx)
	store.Close()
	if err != nil {
		l.t.Fatal(err)
	}
	l.secrets = append(l.secrets, connection.Credential)
	return nodeLifecycleAgent{Connection: connection, directory: directory}
}
func (l *nodeLifecycleLab) serve(node nodeLifecycleAgent) *nodeLifecycleProcess {
	return l.start(node.Name, "agent", "serve", "--data-dir", node.directory, "--listen", "127.0.0.1:0", "--heartbeat-interval", "1s")
}
func (l *nodeLifecycleLab) assertRevoked(node nodeLifecycleAgent) {
	l.t.Helper()
	request, err := http.NewRequestWithContext(l.ctx, "POST", l.origin+"/api/v1/agents/"+node.AgentID+"/heartbeat", strings.NewReader("{}"))
	if err != nil {
		l.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+node.Credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := l.client.Do(request)
	if err != nil {
		l.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		l.t.Fatalf("removed identity heartbeat returned %d", response.StatusCode)
	}
	if l.node(node.AgentID) != nil {
		l.t.Fatal("removed node still appears in inventory")
	}
}
func (l *nodeLifecycleLab) assertPeer(peer nodeLifecycleAgent, p *nodeLifecycleProcess, after time.Time) {
	l.t.Helper()
	l.wait("unaffected peer advances heartbeat", func() bool {
		p.alive(l.t)
		node := l.node(peer.AgentID)
		if node == nil || node.CredentialRevoked {
			l.t.Fatal("unrelated node was removed or revoked")
		}
		return node.Connected && node.LastSeenAt.After(after)
	})
}
func (l *nodeLifecycleLab) assertRestartRejected(node nodeLifecycleAgent, p *nodeLifecycleProcess) {
	l.t.Helper()
	// Require evidence from the restarted CLI itself, not merely a live PID
	// and a rejection sent by the parent test.
	l.wait("restarted Agent reports authentication rejection", func() bool {
		p.alive(l.t)
		log, err := os.ReadFile(p.logPath)
		if err != nil {
			l.t.Fatal(err)
		}
		return bytes.Contains(log, []byte(`"event":"control_plane.heartbeat"`)) &&
			bytes.Contains(log, []byte("Sign in to continue."))
	})
	l.assertRevoked(node)
}
