package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/agent"
)

// This is an explicit local, one-time installation operation, not a new remote
// task kind, product callback, lifecycle RPC or executable catalog script.
func runStageNativePackage(arguments []string) error {
	if err := requireLinuxRoot("agent stage-native-package"); err != nil {
		return err
	}
	flags := flag.NewFlagSet("agent stage-native-package", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	directory := flags.String("data-dir", "", "stopped Agent state directory")
	repository := flags.String("catalog-repository", "", "local signed official repository")
	rootPath := flags.String("catalog-root", "", "independently provisioned official trust root")
	inputPath := flags.String("input", "", "private reviewed installation input")
	resume := flags.Bool("resume-after-review", false, "resume the same interrupted stage after inspecting retained resources")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || *directory == "" || *repository == "" || *rootPath == "" || *inputPath == "" {
		return errors.New("data-dir, catalog-repository, catalog-root and private input are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// The ordinary Agent must not race its local registration or revive an old
	// runtime during handover. Do not stop, mask or restart it from this command.
	inspection, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "vastora-agent.service", "--property=LoadState,ActiveState,SubState,MainPID,ControlPID").Output()
	if err != nil || !nativeStageAgentStopped(string(inspection)) {
		return errors.New("keep the owned Agent service stopped throughout local native handover")
	}
	info, err := os.Lstat(*inputPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4<<20 {
		return errors.New("private bounded native installation input required")
	}
	raw, err := os.ReadFile(*inputPath)
	if err != nil {
		return err
	}
	defer clear(raw)
	var input struct {
		TaskID                 string          `json:"taskId"`
		ApplicationID          string          `json:"applicationId"`
		AppKey                 string          `json:"appKey"`
		ManifestSHA256         string          `json:"manifestSha256"`
		SourceSHA256           string          `json:"sourceSha256"`
		AuthorizedCapabilities []string        `json:"authorizedCapabilities"`
		Config                 json.RawMessage `json:"config"`
		Secrets                json.RawMessage `json:"secrets"`
		ServiceAddress         string          `json:"serviceAddress"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF || !strings.HasPrefix(input.AppKey, "vastora-official/") {
		return errors.New("reviewed official installation input required")
	}
	root, err := os.ReadFile(*rootPath)
	if err != nil {
		return err
	}
	verified, err := catalog.VerifyOfficialRepository(ctx, *repository, "stable", root)
	if err != nil {
		return errors.New("official repository verification failed; no source resources changed")
	}
	var manifest catalog.AppManifest
	for _, candidate := range verified.Catalog.Apps {
		if "vastora-official/"+candidate.ID == input.AppKey {
			manifest = candidate
			break
		}
	}
	canonical, err := catalog.CanonicalAppManifest(manifest)
	if err != nil {
		return errors.New("requested native application is absent from the verified repository")
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if input.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("reviewed native manifest digest differs from the verified catalog")
	}
	store, err := agent.Open(*directory)
	if err != nil {
		return err
	}
	defer store.Close()
	task := agent.DeploymentTask{ID: input.TaskID, Kind: "application.apply", ApplicationID: input.ApplicationID, AppKey: input.AppKey,
		Operation: "install", Manifest: canonical, PackageRevision: canonical.PackageRevision, ManifestSHA256: input.ManifestSHA256,
		AuthorizedCapabilities: input.AuthorizedCapabilities, Config: input.Config, Secrets: input.Secrets, ServiceAddress: input.ServiceAddress}
	executor := agent.ApplicationExecutor{Store: store, Host: agent.SystemdHostApplicationManager{}}
	stage, err := executor.StageNativePackage(ctx, task, input.SourceSHA256, *resume)
	if err != nil {
		// Driver errors may contain paths or values. Keep detailed local evidence
		// private rather than printing application secrets on an installer console.
		return errors.New("native staging incomplete; inspect retained private evidence, keep Agent stopped and do not switch public bindings")
	}
	fmt.Printf("Native installation staged without startup (%d resources); original registration and recovery data retained. Ownership and public binding handover remain incomplete.\n", len(stage.Candidate.Resources))
	return nil
}

func nativeStageAgentStopped(raw string) bool {
	want := map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead", "MainPID": "0", "ControlPID": "0"}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		expected, known := want[key]
		if !ok || !known || seen[key] || value != expected {
			return false
		}
		seen[key] = true
	}
	return len(seen) == len(want)
}
