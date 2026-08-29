package deployer

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestGeneratedHeadscaleConfigAcceptedByPinnedBinary(t *testing.T) {
	if os.Getenv("VASTORA_HEADSCALE_CONFIG_INTEGRATION") != "1" {
		t.Skip("set VASTORA_HEADSCALE_CONFIG_INTEGRATION=1 to validate with the pinned Headscale image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	pull, err := docker.ImagePull(ctx, DefaultHeadscaleImage, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull pinned Headscale image: %v", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	installer := DockerHeadscaleInstaller{HeadscaleImage: DefaultHeadscaleImage}
	files := map[string][]byte{
		"config.yaml":   renderHeadscaleConfig("https://headscale.example.com"),
		"derp.yaml":     renderHeadscaleDERPMap(),
		"policy.hujson": renderHeadscalePolicy(),
	}
	if err := installer.validateHeadscaleConfig(ctx, docker, files); err != nil {
		t.Fatalf("pinned Headscale rejected generated configuration: %v", err)
	}
	broken := make(map[string][]byte, len(files))
	for name, content := range files {
		broken[name] = append([]byte(nil), content...)
	}
	broken["config.yaml"] = []byte(strings.Replace(string(files["config.yaml"]), "  urls: []", "\turls: []", 1))
	if err := installer.validateHeadscaleConfig(ctx, docker, broken); err == nil {
		t.Fatal("pinned Headscale accepted malformed candidate configuration")
	}
}

func TestHeadscaleCandidateRejectsMalformedYAMLBeforeBinaryValidation(t *testing.T) {
	files := map[string][]byte{
		"config.yaml":   []byte("server_url: https://headscale.example.com\n\tderp: {}\n"),
		"derp.yaml":     renderHeadscaleDERPMap(),
		"policy.hujson": renderHeadscalePolicy(),
	}
	if err := validateHeadscaleCandidateFiles(files); err == nil {
		t.Fatal("malformed Headscale YAML was accepted")
	}
}

func TestHeadscaleConfigSnapshotRestoresPreviousFiles(t *testing.T) {
	directory := t.TempDir()
	installer := DockerHeadscaleInstaller{ConfigDir: directory}
	if err := os.WriteFile(directory+"/config.yaml", []byte("old config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installer.snapshotHeadscaleConfig(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory+"/config.yaml", []byte("new config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory+"/derp.yaml", []byte("new derp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacement := headscaleReplacement{installer: installer}
	if err := replacement.restoreConfig(); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(directory + "/config.yaml")
	if err != nil || string(config) != "old config\n" {
		t.Fatalf("restored config = %q, err=%v", config, err)
	}
	if _, err := os.Stat(directory + "/derp.yaml"); !os.IsNotExist(err) {
		t.Fatalf("new DERP map survived rollback: %v", err)
	}
}
