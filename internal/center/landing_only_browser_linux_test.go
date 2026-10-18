package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/deployapi"
)

// Dedicated live backend for browser acceptance; never uses production state.
func TestLandingOnlyBrowserLab(t *testing.T) {
	host, _ := os.Hostname()
	if os.Getenv("VASTORA_DISPOSABLE_PRIVATE_LAB") != "issue-940" {
		t.Skip("isolated lab only")
	}
	if runtime.GOOS != "linux" || host != "vastora-940-control-test" || os.Geteuid() != 0 {
		t.Fatal("wrong lab")
	}
	root := "/opt/vastora940"
	ctx := context.Background()
	s, err := Open(filepath.Join(root, "browser-state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	Version = "0.1.0-940lab.2"
	trust, err := os.ReadFile(filepath.Join(root, "catalog-root.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureOfficialCatalog(ctx, catalog.OfficialOrigin); err != nil {
		t.Fatal(err)
	}
	helpers, err := s.ConfigureExternalHelpers(ExternalHelperConfig{PublicHelperOrigin: "https://vastora.petauron.com", RegionLookupURL: "https://api.country.is"})
	if err != nil {
		t.Fatal(err)
	}
	app := NewServer(s, filepath.Join(root, "web"), true).WithPublicAddressLookupURL(helpers.PublicAddressLookupURL, helpers.PublicHelperAllowPrivate).WithOfficialCatalogTrust(catalog.OfficialOrigin, trust).WithInfrastructureManager(privateLabInfrastructure{}).WithAgentBinaries(filepath.Join(root, "distribution")).Handler()
	target, _ := url.Parse("http://127.0.0.1:8081")
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := &http.Server{Addr: ":443", ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.Host, ":443") == "headscale.vastora940.test" && !strings.HasPrefix(r.URL.Path, "/install/") && !strings.HasPrefix(r.URL.Path, "/api/v1/agent") {
			proxy.ServeHTTP(w, r)
			return
		}
		app.ServeHTTP(w, r)
	})}
	defer server.Close()
	go func() {
		if err := server.ListenAndServeTLS(filepath.Join(root, "lab.crt"), filepath.Join(root, "lab.key")); err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	time.Sleep(200 * time.Millisecond)
	s.builtinHeadscaleDialAddress = "127.0.0.1:443"
	key, err := os.ReadFile(filepath.Join(root, "headscale-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "headscale-api-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []struct {
		ID         uint64
		Prefix     string
		Expiration struct{ Seconds int64 }
	}
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 1 {
		t.Fatal("key metadata")
	}
	if _, err = s.ConfigureBuiltinHeadscale(ctx, deployapi.HeadscaleInstallResult{Endpoint: "https://headscale.vastora940.test", APIKey: strings.TrimSpace(string(key)), APIKeyID: keys[0].ID, APIKeyPrefix: strings.TrimSuffix(keys[0].Prefix, "***"), APIKeyExpiresAt: time.Unix(keys[0].Expiration.Seconds, 0)}, deployapi.HeadscaleDNSPolicySystem, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "browser-password")); os.IsNotExist(err) {
		password, e := randomToken(32)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = s.CreateFirstAdmin(ctx, "lab", password); e != nil {
			t.Fatal(e)
		}
		if _, e = s.CompleteInitialSetup(ctx, InitialSetupInput{Site: SiteInput{Name: "Isolated Lab", Code: "browser-lab", Timezone: "UTC"}, Network: CenterNetworkInput{AgentConnectionMode: "public", AgentConnectURL: "https://center.vastora940.test"}}); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "browser-password"), []byte(password), 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("browser lab ready")
	for i := 0; i < 3600; i++ {
		if _, e := os.Stat(filepath.Join(root, "browser-done")); e == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("browser lab timed out")
}
