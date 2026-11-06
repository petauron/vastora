// alpine-acceptance is an opt-in fixture for an explicitly provisioned,
// disposable Alpine test guest. It is not included in release distributions.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/center"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/hostservice"
)

const root = "/var/lib/vastora-alpine-acceptance"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	marker, err := os.ReadFile("/etc/vastora-isolated-test")
	if err != nil || string(marker) != "alpine-openrc\n" || os.Geteuid() != 0 {
		return errors.New("isolated Alpine test marker and root required")
	}
	if kind, err := hostservice.Detect(); err != nil || kind != hostservice.OpenRC {
		return errors.New("running OpenRC required")
	}
	if len(os.Args) < 2 {
		return errors.New("serve, enroll, verify-agent, or deploy TASK_FILE required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	ctx := context.Background()
	if os.Args[1] == "deploy" {
		var task agent.DeploymentTask
		reviewed := len(os.Args) == 5 && os.Args[4] == "recover-upgrade"
		if len(os.Args) == 3 {
			raw, err := os.ReadFile(os.Args[2])
			if err != nil {
				return err
			}
			if json.Unmarshal(raw, &task) != nil {
				return errors.New("invalid synthetic task")
			}
		} else if len(os.Args) == 5 {
			raw, err := os.ReadFile(os.Args[2])
			if err != nil {
				return err
			}
			if json.Unmarshal(raw, &task.Manifest) != nil {
				return errors.New("invalid candidate manifest")
			}
			raw, err = os.ReadFile(os.Args[3])
			if err != nil {
				return err
			}
			var inputs struct {
				Config  map[string]any    `json:"config"`
				Secrets map[string]string `json:"secrets"`
			}
			if json.Unmarshal(raw, &inputs) != nil {
				return errors.New("invalid private fixture inputs")
			}
			task.Config, _ = json.Marshal(inputs.Config)
			task.Secrets, _ = json.Marshal(inputs.Secrets)
			task.Operation = os.Args[4]
			if reviewed {
				task.Operation = "upgrade"
			}
			if task.Operation != "install" && task.Operation != "upgrade" {
				return errors.New("install or upgrade only")
			}
			task.ID = fmt.Sprintf("acceptance-%s-%s-%d", task.Manifest.ID, task.Operation, task.Manifest.PackageRevision)
			task.ApplicationID = task.Manifest.ID + "-alpine-fixture"
			task.AppKey = "vastora-official/" + task.Manifest.ID
		} else {
			return errors.New("deploy TASK_FILE or deploy MANIFEST INPUTS install|upgrade required")
		}

		manifest, err := catalog.CanonicalAppManifest(task.Manifest)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		task.Manifest = manifest
		task.ManifestSHA256 = hex.EncodeToString(digest[:])
		task.PackageRevision = manifest.PackageRevision
		task.AuthorizedCapabilities = append([]string{}, manifest.Runtime.RequiredCapabilities...)
		client, err := assetClient()
		if err != nil {
			return err
		}
		executor := agent.ApplicationExecutor{Host: agent.SystemdHostApplicationManager{InitSystem: hostservice.OpenRC, HTTPClient: client}, PackageStateDirectory: filepath.Join(root, "packages")}
		if reviewed {
			receipt, err := executor.ApplicationResources(task.ApplicationID)
			if err != nil {
				return err
			}
			if receipt.State != "review-required" {
				return errors.New("fixture recovery requires an actual review-required receipt")
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			digest, err := controlplane.PackageReceiptDigest(raw)
			if err != nil {
				return err
			}
			task.Kind = "application.apply"
			task.Attempt = 1
			task.Authorization = controlplane.ExecutionAuthorization{ID: "isolated-reviewed-recovery", Protocol: controlplane.ExecutionProtocol, Digest: task.ManifestSHA256}
			task.PackageRecovery = &controlplane.PackageRecovery{TaskID: receipt.TaskID, ExecutionID: "isolated-abandoned-execution", Attempt: 1, ReceiptSHA256: digest}
		}
		result, err := executor.Deploy(ctx, task)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "last-result.json"), raw, 0600); err != nil {
			return err
		}
		fmt.Println("native task completed; receipt saved privately")
		return nil
	}
	store, err := center.Open(filepath.Join(root, "center"))
	if err != nil {
		return err
	}
	defer store.Close()
	// This loopback-only fixture has no public onboarding UI. Populate only its
	// two required network settings; never replace an existing value/identity.
	db, err := sql.Open("sqlite", filepath.Join(root, "center", "center.db"))
	if err != nil {
		return err
	}
	for key, value := range map[string]string{"agent_connection_mode": "lan", "agent_connect_url": "http://127.0.0.1:18080"} {
		if _, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)", key, value); err != nil {
			db.Close()
			return err
		}
	}
	if err := db.Close(); err != nil {
		return err
	}
	switch os.Args[1] {
	case "serve":
		server := center.NewServer(store, "", false).WithAgentBinaries(filepath.Join(root, "binaries"))
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		protocols.SetUnencryptedHTTP2(true)
		web := &http.Server{Addr: "127.0.0.1:18080", Handler: server.Handler(), Protocols: protocols, HTTP2: &http.HTTP2Config{MaxConcurrentStreams: 32}, ReadHeaderTimeout: 5 * time.Second}
		assets, err := assetServer()
		if err != nil {
			return err
		}
		defer web.Close()
		defer assets.Close()
		proxy := senlunProxy()
		defer proxy.Close()
		failures := make(chan error, 3)
		go func() {
			failures <- proxy.ListenAndServeTLS(filepath.Join(root, "asset-leaf.pem"), filepath.Join(root, "asset-leaf-key.pem"))
		}()
		go func() { failures <- web.ListenAndServe() }()
		go func() {
			failures <- assets.ListenAndServeTLS(filepath.Join(root, "asset-leaf.pem"), filepath.Join(root, "asset-leaf-key.pem"))
		}()
		return <-failures

	case "enroll":
		sites, err := store.ListSites(ctx)
		if err != nil {
			return err
		}
		if len(sites) == 0 {
			site, err := store.CreateSite(ctx, center.SiteInput{Name: "Alpine isolated", Code: "alpine", Timezone: "UTC"})
			if err != nil {
				return err
			}
			sites = append(sites, site)
		}
		enrollment, err := store.CreateAgentEnrollment(ctx, center.AgentEnrollmentSpec{SiteID: sites[0].ID, Name: "Alpine test agent", CenterURL: "http://127.0.0.1:18080"})
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "enrollment-token"), []byte(enrollment.Token), 0600)
	case "queue-update":
		agents, err := store.ListAgents(ctx)
		if err != nil {
			return err
		}
		if len(agents) != 1 {
			return errors.New("one fixture Agent required")
		}
		_, err = store.QueueAgentUpdate(ctx, agents[0].ID, center.Version)
		if err == nil {
			fmt.Println("managed isolated Agent update queued")
		}
		return err
	case "verify-agent":
		agents, err := store.ListAgents(ctx)
		if err != nil {
			return err
		}
		if len(agents) != 1 || !agents[0].Connected || agents[0].Capabilities.ExecutorVersions["native"] != 2 {
			return errors.New("isolated Agent is not connected")
		}
		fmt.Printf("agent connected; version=%s; native-v2=%d\n", agents[0].Version, agents[0].Capabilities.ExecutorVersions["native"])
		return nil
	}
	return errors.New("unknown acceptance command")
}
