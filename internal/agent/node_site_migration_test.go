package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/nodesite"
)

func migrationTestSite(t *testing.T, hostname string) (CaddySite, nodesite.Task) {
	t.Helper()
	key, private := nodeSiteTestKey(t)
	chain := nodeSiteTestChain(t, key, hostname)
	pair, err := tls.X509KeyPair([]byte(chain), []byte(private))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != hostname {
			http.Error(w, "wrong hostname", 400)
			return
		}
		w.Write([]byte("owned site"))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	t.Cleanup(server.Close)
	_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	certificate, err := nodeSiteCertificate(hostname, chain, private)
	if err != nil {
		t.Fatal(err)
	}
	site := CaddySite{ApplicationID: "site-instance", Hostname: hostname, Address: "127.0.0.1", HTTPPort: 24481, TLSPort: port, HTML: "owned site", Certificate: certificate}
	task := nodesite.Task{ApplicationID: site.ApplicationID, AppKey: "vastora-official/daylight", Hostname: hostname, Port: port, Revision: 1, Action: "apply", CertificatePEM: chain, PrivateKeyPEM: private}
	return site, task
}

func TestNodeSiteHostnameMigrationKeepsOldSiteAndReplaysCompletion(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, oldTask := migrationTestSite(t, "us1.example.com")
	next, task := migrationTestSite(t, "us-abcdef.example.com")
	if err = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: old.ApplicationID, State: "applied", Site: &old}); err != nil {
		t.Fatal(err)
	}
	if err = s.saveNodeSiteJournal(ctx, nodeSiteJournal{Runtime: 2, Task: oldTask, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	s.sharedCaddy = &CaddyGatewayDriver{state: gateway.DesiredState{Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.2", HTTPSPort: 443, HTTPPort: 80}}}}
	loads := 0
	s.reloadSharedCaddy = func(ctx context.Context) error {
		loads++
		sites, err := s.SharedCaddySites(ctx)
		if err != nil {
			return err
		}
		_, err = composeCaddySites(nodeSiteTestConfig(), s.sharedCaddy.state, sites, false)
		return err
	}
	task.Action, task.PreviousHostname, task.Revision = "prepare-migration", old.Hostname, 2
	r, err := s.executeWebsiteMigration(ctx, task, nodeSiteHost{site: old})
	if err != nil || r.State != "prepared" {
		t.Fatal("prepare", r, err)
	}
	sites, err := s.SharedCaddySites(ctx)
	if err != nil || len(sites) != 2 {
		t.Fatal("old site was lost", sites, err)
	}
	if _, err = s.executeWebsiteMigration(ctx, task, nodeSiteHost{site: old}); err != nil {
		t.Fatal("lost prepare response", err)
	}
	if loads < 2 {
		t.Fatal("retry did not reconcile the durable candidate")
	}
	accept := task
	accept.Action = "accept-migration"
	accept.CertificatePEM, accept.PrivateKeyPEM = "", ""
	if _, err = s.executeWebsiteMigration(ctx, accept, nodeSiteHost{site: old}); err == nil {
		t.Fatal("accepted before installed binding changed")
	}
	prepared, err := s.preparedWebsiteBinding(ctx, old.ApplicationID, next.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.updateCaddySite(ctx, old.ApplicationID, &prepared, func(ctx context.Context) error { return probeCaddySite(ctx, prepared) }); err != nil {
		t.Fatal(err)
	}
	if sites, err = s.SharedCaddySites(ctx); err != nil || len(sites) != 2 {
		t.Fatal("old binding was retired before acceptance", err)
	}
	// The package's base Daylight configuration has no HTTPSListen. A later
	// content refresh must not erase the migration-owned public route, even
	// when it is reconciled repeatedly before acceptance.
	for range 2 {
		if err = s.syncSharedDaylightHost(ctx, AppliedInstallation{ApplicationID: old.ApplicationID}, nodeSiteHost{site: next}); err != nil {
			t.Fatal("refresh prepared website", err)
		}
		current, err := s.readCaddySite(ctx, old.ApplicationID)
		if err != nil || current.Site == nil || current.Site.PublicListen != "203.0.113.2:443" || current.Site.Certificate.Hostname != next.Hostname {
			t.Fatal("content refresh lost the prepared public TLS binding", err)
		}
	}

	reload := s.reloadSharedCaddy
	failedLoads := 0
	s.reloadSharedCaddy = func(ctx context.Context) error {
		failedLoads++
		if failedLoads == 1 {
			return errors.New("controlled acceptance reload failure")
		}
		return reload(ctx)
	}
	if _, err = s.executeWebsiteMigration(ctx, accept, nodeSiteHost{site: prepared}); err == nil || failedLoads != 2 {
		t.Fatal("acceptance did not restore and reload the prepared configuration", err, failedLoads)
	}
	if sites, err = s.SharedCaddySites(ctx); err != nil || len(sites) != 2 {
		t.Fatal("failed acceptance lost the old binding", err)
	}
	s.reloadSharedCaddy = reload
	if _, err = s.executeWebsiteMigration(ctx, accept, nodeSiteHost{site: prepared}); err != nil {
		t.Fatal("accept", err)
	}
	if _, err = s.executeWebsiteMigration(ctx, accept, nodeSiteHost{site: prepared}); err != nil {
		t.Fatal("lost accept response", err)
	}
	if sites, err = s.SharedCaddySites(ctx); err != nil || len(sites) != 1 || sites[0].Hostname != next.Hostname {
		t.Fatal("accepted binding", sites, err)
	}
	// A later preparation can be cancelled without dropping the active binding,
	// including a retry after the cancellation response was lost.
	_, later := migrationTestSite(t, "us-next.example.com")
	later.Action, later.PreviousHostname, later.Revision = "prepare-migration", prepared.Hostname, 3
	if _, err = s.executeWebsiteMigration(ctx, later, nodeSiteHost{site: prepared}); err != nil {
		t.Fatal(err)
	}
	cancel := later
	cancel.Action = "cancel-migration"
	cancel.CertificatePEM, cancel.PrivateKeyPEM = "", ""
	for range 2 {
		if _, err = s.executeWebsiteMigration(ctx, cancel, nodeSiteHost{site: prepared}); err != nil {
			t.Fatal("cancel replay", err)
		}
	}
	if sites, err = s.SharedCaddySites(ctx); err != nil || len(sites) != 1 || sites[0].Hostname != prepared.Hostname {
		t.Fatal("cancel removed the active binding", sites, err)
	}
}

func TestNodeSiteHostnameMigrationFailureRestoresOriginalJournal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, oldTask := migrationTestSite(t, "us1.example.com")
	_, task := migrationTestSite(t, "us-abcdef.example.com")
	previous := nodeSiteJournal{Runtime: 2, Task: oldTask, State: "applied"}
	if err = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: old.ApplicationID, State: "applied", Site: &old}); err != nil {
		t.Fatal(err)
	}
	if err = s.saveNodeSiteJournal(ctx, previous); err != nil {
		t.Fatal(err)
	}
	s.sharedCaddy = &CaddyGatewayDriver{}
	loads := 0
	s.reloadSharedCaddy = func(context.Context) error {
		loads++
		if loads == 1 {
			return errors.New("controlled candidate load failure")
		}
		return nil
	}
	task.Action, task.PreviousHostname, task.Revision = "prepare-migration", old.Hostname, 2
	if _, err = s.executeWebsiteMigration(ctx, task, nodeSiteHost{site: old}); err == nil {
		t.Fatal("failed load reported ready")
	}
	after, err := s.readNodeSiteJournal(ctx, old.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(previous)
	b, _ := json.Marshal(after)
	if string(a) != string(b) || loads != 2 {
		t.Fatal("original certificate journal was not restored")
	}
	if err = probeCaddySite(ctx, old); err != nil {
		t.Fatal("old website unavailable", err)
	}
}

func TestNodeSiteHostnameConfigureRetainsOtherInputsAndSecrets(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := migrationTestSite(t, "us1.example.com")
	next, task := migrationTestSite(t, "us-random.example.com")
	task.Action, task.PreviousHostname, task.Revision = "prepare-migration", old.Hostname, 2
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	beforeConfig := json.RawMessage(`{"hostname":"us1.example.com","http_port":24481}`)
	if _, err = s.RecordApplied(ctx, AppliedInstallation{InstanceID: "original", ApplicationID: old.ApplicationID, AppKey: task.AppKey, Version: "1.0.0", ServiceAddress: "127.0.0.1", Config: beforeConfig, Secrets: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	backend := NativeServicesPackageBackend{Store: s}
	var manifest catalog.AppManifest
	if err = json.Unmarshal([]byte(`{"version":"1.0.0","runtime":{"kind":"native","version":2,"requiredCapabilities":["native-services","shared-caddy"]}}`), &manifest); err != nil {
		t.Fatal(err)
	}
	change := DeploymentTask{Operation: "configure", ApplicationID: old.ApplicationID, AppKey: task.AppKey, Manifest: manifest, ManifestSHA256: sha256String([]byte("reviewed manifest")), PackageRevision: 1, ServiceAddress: "127.0.0.1", Config: json.RawMessage(`{"hostname":"us-random.example.com","http_port":24481}`), Secrets: json.RawMessage(`{}`)}
	receipt := &InstanceResources{State: "ready", ManifestSHA256: change.ManifestSHA256, PackageVersion: manifest.Version, PackageRevision: change.PackageRevision}
	if err = backend.verifyWebsiteConfiguration(ctx, change, receipt); err == nil {
		t.Fatal("unprepared hostname accepted")
	}
	if err = s.saveNodeSiteJournal(ctx, nodeSiteJournal{Runtime: 2, Task: task, State: "prepared", Previous: a, Candidate: b}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"native", "systemd"} {
		change.Manifest.Runtime.Kind = kind
		if !sharedWebsiteConfigure(change) {
			t.Fatal("existing reviewed v2 website executor refused", kind)
		}
		if err = backend.verifyWebsiteConfiguration(ctx, change, receipt); err != nil {
			t.Fatal(kind, err)
		}
	}
	// A failed configure retains its installed configuration and migration.
	// Only the operator-reviewed next attempt may use that ownership receipt.
	retry := change
	retry.ID, retry.Kind, retry.Attempt = "website-configure", "application.apply", 2
	retry.Authorization = controlplane.ExecutionAuthorization{ID: "new-execution", Protocol: controlplane.ExecutionProtocol, Digest: sha256String([]byte("retry authorization"))}
	retry.PackageRecovery = &controlplane.PackageRecovery{ExecutionID: "failed-execution", TaskID: retry.ID, Attempt: 1}
	retained := *receipt
	retained.State, retained.TaskID = "review-required", retry.ID
	retained.Resources = []RuntimeResource{{Kind: "file", Path: "/owned/website-config"}}
	if err = backend.verifyWebsiteConfiguration(ctx, retry, &retained); err != nil {
		t.Fatal("reviewed configure retry refused", err)
	}
	for _, mutate := range []func(*DeploymentTask){
		func(v *DeploymentTask) { v.PackageRecovery = nil },
		func(v *DeploymentTask) { v.Attempt++ },
		func(v *DeploymentTask) { v.ID = "different-task" },
		func(v *DeploymentTask) { v.ManifestSHA256 = sha256String([]byte("different retry package")) },
	} {
		invalid := retry
		mutate(&invalid)
		if err = backend.verifyWebsiteConfiguration(ctx, invalid, &retained); err == nil {
			t.Fatal("unreviewed or changed configure retry accepted")
		}
	}
	for _, mutate := range []func(*DeploymentTask){
		func(v *DeploymentTask) {
			v.Config = json.RawMessage(`{"hostname":"us-random.example.com","http_port":24490}`)
		},
		func(v *DeploymentTask) { v.Secrets = json.RawMessage(`{"unexpected":"new value"}`) },
		func(v *DeploymentTask) { v.ManifestSHA256 = sha256String([]byte("different manifest")) },
	} {
		invalid := change
		mutate(&invalid)
		if err = backend.verifyWebsiteConfiguration(ctx, invalid, receipt); err == nil {
			t.Fatal("migration changed more than its hostname")
		}
	}
	installed, err := s.AppliedInstallation(ctx, task.AppKey)
	if err != nil || string(installed.Config) != string(beforeConfig) {
		t.Fatal("validation modified the installed configuration", err)
	}
}
