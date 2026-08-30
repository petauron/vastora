package deployer

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestCommittedGatewayReadyRequiresDurableCommitAndHealthyCandidate(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "vgw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "admin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	journal := gatewayReplacementJournal{Phase: "committed", CandidateID: "candidate"}
	current := func(state *container.State) *client.ContainerInspectResult {
		return &client.ContainerInspectResult{Container: container.InspectResponse{ID: "candidate", State: state}}
	}
	for name, test := range map[string]struct {
		journal gatewayReplacementJournal
		current *client.ContainerInspectResult
		socket  string
		ready   bool
	}{
		"healthy committed":      {journal, current(&container.State{Running: true, Health: &container.Health{Status: container.Healthy}}), socket, true},
		"running no healthcheck": {journal, current(&container.State{Running: true}), socket, true},
		"uncommitted":            {gatewayReplacementJournal{Phase: "prepared", CandidateID: "candidate"}, current(&container.State{Running: true}), socket, false},
		"stopped":                {journal, current(&container.State{}), socket, false},
		"restarting":             {journal, current(&container.State{Running: true, Restarting: true}), socket, false},
		"unhealthy":              {journal, current(&container.State{Running: true, Health: &container.Health{Status: container.Unhealthy}}), socket, false},
		"starting":               {journal, current(&container.State{Running: true, Health: &container.Health{Status: container.Starting}}), socket, false},
		"wrong generation":       {journal, &client.ContainerInspectResult{Container: container.InspectResponse{ID: "other", State: &container.State{Running: true}}}, socket, false},
		"missing admin socket":   {journal, current(&container.State{Running: true}), filepath.Join(directory, "missing.sock"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := committedGatewayReady(test.journal, test.current, test.socket); got != test.ready {
				t.Fatalf("ready=%v want=%v", got, test.ready)
			}
		})
	}
}

func TestGatewayReplacementJournalSurvivesRestartAndRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), gatewayReplacementJournalFile)
	replacement := gatewayReplacement{
		generation: "generation-1", journalPath: path, candidateID: "candidate", backupID: "healthy-backup",
		legacyID: "legacy", layer4ID: "layer4", backupWasRunning: true, legacyWasRunning: true, layer4WasRunning: true,
	}
	if err := replacement.persist("prepared"); err != nil {
		t.Fatal(err)
	}
	prepared, found, err := loadGatewayReplacementJournal(path)
	if err != nil || !found || prepared.Phase != "prepared" || prepared.BackupID != "healthy-backup" || prepared.CandidateID != "candidate" || !prepared.BackupWasRunning {
		t.Fatalf("prepared journal did not replay: journal=%#v found=%v err=%v", prepared, found, err)
	}
	if err := replacement.persist("committed"); err != nil {
		t.Fatal(err)
	}
	committed, found, err := loadGatewayReplacementJournal(path)
	if err != nil || !found || committed.Phase != "committed" || committed.Generation != replacement.generation {
		t.Fatalf("committed journal did not replay: journal=%#v found=%v err=%v", committed, found, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"phase":"committed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadGatewayReplacementJournal(path); err == nil {
		t.Fatal("corrupt committed journal was accepted")
	}
}
