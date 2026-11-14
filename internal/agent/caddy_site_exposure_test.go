package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/petauron/vastora/internal/gateway"
	"strings"
	"testing"
)

func TestNodeSiteLoopbackTLSHandoffPreservesCertificateAndRecovers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	site, task := migrationTestSite(t, "site.example.test")
	site.PublicListen = "203.0.113.2:443"
	if err = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: site.ApplicationID, State: "applied", Site: &site}); err != nil {
		t.Fatal(err)
	}
	if err = s.saveNodeSiteJournal(ctx, nodeSiteJournal{Runtime: 2, Task: task, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	s.sharedCaddy = &CaddyGatewayDriver{}
	loads := 0
	fail := true
	s.reloadSharedCaddy = func(ctx context.Context) error {
		loads++
		sites, err := s.SharedCaddySites(ctx)
		if err != nil {
			return err
		}
		raw, err := composeCaddySites(nodeSiteTestConfig(), gateway.DesiredState{}, sites, false)
		if err != nil {
			return err
		}
		if fail && loads == 1 {
			return errors.New("controlled load failure")
		}
		if sites[0].TLSLoopbackOnly && strings.Contains(string(raw), "203.0.113.2:443") {
			return errors.New("delegated public socket retained")
		}
		return nil
	}
	task.Revision = 2
	task.TLSLoopbackOnly = true
	if _, err = s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site}); err == nil {
		t.Fatal("failed handoff accepted")
	}
	state, err := s.readCaddySite(ctx, site.ApplicationID)
	if err != nil || state.Site.TLSLoopbackOnly || state.Site.Certificate.Hostname != site.Hostname {
		t.Fatal("old public website was not restored", err)
	}
	fail = false
	task.Revision = 3
	r, err := s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site})
	if err != nil || !r.TLSLoopbackOnly || r.Fingerprint == "" {
		t.Fatal("loopback handoff", r, err)
	}
	state, err = s.readCaddySite(ctx, site.ApplicationID)
	if err != nil || !state.Site.TLSLoopbackOnly || state.Site.PublicListen != site.PublicListen {
		t.Fatal("lost original socket recovery identity", err)
	}
	if err = s.syncSharedDaylightHost(ctx, AppliedInstallation{ApplicationID: site.ApplicationID}, nodeSiteHost{site: site}); err != nil {
		t.Fatal(err)
	}
	state, _ = s.readCaddySite(ctx, site.ApplicationID)
	if !state.Site.TLSLoopbackOnly {
		t.Fatal("content refresh reclaimed443")
	}
	// A same-revision tampering attempt cannot reclaim public TLS with the same cert.
	tampered := task
	tampered.TLSLoopbackOnly = false
	if _, err = s.executeNodeSiteOnHost(ctx, tampered, nodeSiteHost{site: site}); err == nil {
		t.Fatal("same-revision exposure changed")
	}
	task.Revision = 4
	task.TLSLoopbackOnly = false
	if _, err = s.executeNodeSiteOnHost(ctx, task, nodeSiteHost{site: site}); err != nil {
		t.Fatal("forward public restore", err)
	}
	state, _ = s.readCaddySite(ctx, site.ApplicationID)
	if state.Site.TLSLoopbackOnly || state.Site.Certificate.CertificatePEM != site.Certificate.CertificatePEM {
		t.Fatal("restore changed certificate")
	}
}
func TestLoopbackTLSCannotWithdrawUnrelatedWebsite(t *testing.T) {
	site, _ := migrationTestSite(t, "site.example.test")
	site.PublicListen = "203.0.113.2:443"
	site.TLSLoopbackOnly = true
	desired := gateway.DesiredState{Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.2", HTTPSPort: 443, HTTPPort: 80}}, Routes: []gateway.Route{{ID: "other", Hostname: "other.example.test", ListenerKind: "public", TLSEnabled: true, Upstreams: []gateway.Upstream{{Address: "100.64.0.9", Port: 8080}}}}}
	if _, err := composeCaddySites(nodeSiteTestConfig(), desired, []CaddySite{site}, true); err == nil {
		t.Fatal("unrelated public website withdrawn")
	}
	// A fully owned route may be delegated while the local TLS server survives.
	desired.Routes[0].Upstreams = []gateway.Upstream{{Address: site.Address, Port: site.HTTPPort}}
	if _, err := composeCaddySites(nodeSiteTestConfig(), desired, []CaddySite{site}, true); err == nil {
		t.Fatal("same upstream adopted another hostname")
	}
	desired.Routes[0].Hostname = site.Hostname
	raw, err := composeCaddySites(nodeSiteTestConfig(), desired, []CaddySite{site}, true)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil || !strings.Contains(string(raw), "site.example.test") {
		t.Fatal("lost owned TLS backend")
	}
}

func TestLoopbackTLSPreservesOtherPublicAddress(t *testing.T) {
	site, _ := migrationTestSite(t, "site.example.test")
	site.PublicListen = "203.0.113.2:443"
	site.TLSLoopbackOnly = true
	desired := gateway.DesiredState{Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.9", HTTPSPort: 443, HTTPPort: 80}}, Routes: []gateway.Route{{ID: "other", Hostname: "other.example.test", ListenerKind: "public", TLSEnabled: true, System: true}}}
	for _, container := range []bool{false, true} {
		listen := "203.0.113.9:443"
		if container {
			listen = "0.0.0.0:443"
		}
		base, _ := json.Marshal(map[string]any{"apps": map[string]any{"http": map[string]any{"servers": map[string]any{"other-public": map[string]any{"listen": []string{listen}}}}}})
		raw, err := composeCaddySites(base, desired, []CaddySite{site}, container)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if json.Unmarshal(raw, &cfg) != nil {
			t.Fatal("invalid Caddy configuration")
		}
		servers := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
		if servers["other-public"] == nil {
			t.Fatal("withdrew unrelated public address", container)
		}
	}
}
