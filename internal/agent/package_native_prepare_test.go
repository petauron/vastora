package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
)

func TestNativePackagePrepareAuthenticatesAndScopesAllServices(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			binary := testArtifactELF(t, arch)
			for _, name := range []string{"controller", "worker"} {
				if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(binary))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(binary); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; _, _ = w.Write(archive.Bytes()) }))
			defer server.Close()
			digest := sha256.Sum256(archive.Bytes())
			app := nativePlanFixture()
			app.ID = "example"
			app.Artifacts = []catalog.Artifact{{Name: "bundle", OperatingSystem: "linux", Architecture: arch, URL: server.URL + "/bundle", SHA256: hex.EncodeToString(digest[:]), Format: "tar.gz"}}
			app.Runtime.NativeServices.Services[0].WritableState = []string{"data"}
			app.Runtime.NativeServices.Services[0].Process.ConfigFiles = []catalog.ConfigFile{{Path: "config.json", Format: "json", Values: map[string]catalog.Value{
				"worker":  {Runtime: &catalog.RuntimeValue{Kind: "native-unit", Name: "worker"}},
				"account": {Runtime: &catalog.RuntimeValue{Kind: "managed-user", Name: "worker"}},
				"state":   {Runtime: &catalog.RuntimeValue{Kind: "state-directory", Name: "data"}},
			}}}
			task := DeploymentTask{ApplicationID: "example-instance", Manifest: app}
			root := packageTestDirectory(t)
			manager := SystemdHostApplicationManager{RootDir: root, HostTarget: platform.Target{OS: "linux", Architecture: arch}, HTTPClient: server.Client(), RunCommand: func(context.Context, string, ...string) error { t.Fatal("prepare executed a host command"); return nil }}
			prepared, err := prepareNativePackage(context.Background(), manager, packageTestDirectory(t), task)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("shared artifact fetched %d times", requests)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatal("prepare mutated host", err)
			}
			for _, unit := range prepared.Plan.Units {
				text := string(prepared.Files[unit.Path].Data)
				if !strings.Contains(text, "ProtectSystem=strict\n") || !strings.Contains(text, "User="+unit.Process.User+"\n") {
					t.Fatal("sandbox/account missing")
				}
				if unit.LogicalName == "controller" {
					if !strings.Contains(text, "ReadWritePaths="+prepared.Plan.State["data"].Path+"\n") || strings.Contains(text, "StateDirectory=") {
						t.Fatal("controller claimed worker state ownership")
					}
					config := prepared.Files[unit.ConfigDirectory+"/config/config.json"]
					if !config.ApplicationOwned || config.Mode != 0600 || !bytes.Contains(config.Data, []byte(prepared.Plan.Users["worker"])) {
						t.Fatal("private bootstrap references missing")
					}
				}
			}
			task.Manifest.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			if _, err := prepareNativePackage(context.Background(), manager, packageTestDirectory(t), task); err == nil {
				t.Fatal("corrupt artifact accepted")
			}
		})
	}
}
