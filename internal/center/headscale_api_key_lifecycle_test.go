package center

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type fakeHeadscaleKeyRotator struct {
	fakeBuiltinHeadscaleInstaller
	rotation       deployapi.HeadscaleAPIKeyRotation
	prepared       []deployapi.HeadscaleAPIKeyRotationRequest
	committed      []deployapi.HeadscaleAPIKeyCommitRequest
	commitFailures int
}

func fakeHeadscaleAPIKey(prefix, fill string) string {
	return "hskey" + "-api-" + prefix + "-" + strings.Repeat(fill, 64)
}

func (rotator *fakeHeadscaleKeyRotator) PrepareHeadscaleAPIKeyRotation(_ context.Context, input deployapi.HeadscaleAPIKeyRotationRequest) (deployapi.HeadscaleAPIKeyRotation, error) {
	rotator.prepared = append(rotator.prepared, input)
	return rotator.rotation, nil
}

func (rotator *fakeHeadscaleKeyRotator) CommitHeadscaleAPIKeyRotation(_ context.Context, input deployapi.HeadscaleAPIKeyCommitRequest) error {
	rotator.committed = append(rotator.committed, input)
	if rotator.commitFailures > 0 {
		rotator.commitFailures--
		return errors.New("commit response lost")
	}
	return nil
}

func TestHeadscaleAPIKeyRotationResumesAfterCommitResponseLoss(t *testing.T) {
	headscale := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user" || request.Header.Get("Authorization") != "Bearer "+fakeHeadscaleAPIKey("newprefix123", "n") {
			t.Fatalf("unexpected replacement key verification: %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		_, _ = writer.Write([]byte(`{"users":[]}`))
	}))
	defer headscale.Close()
	endpoint := "https://example.com:" + strings.Split(headscale.Listener.Addr().String(), ":")[1]
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.headscaleHTTPClient = headscale.Client()
	store.builtinHeadscaleDialAddress = headscale.Listener.Addr().(*net.TCPAddr).String()
	oldKey := fakeHeadscaleAPIKey("oldprefix123", "o")
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	oldSecretID, err := store.putSecret(context.Background(), tx, []byte(oldKey), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at)
		VALUES('headscale', 'builtin', ?, ?, 'configured', ?, ?)`, endpoint, oldSecretID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO headscale_api_keys(id, key_id, key_prefix, expires_at, state, updated_at)
		VALUES(1, 1, 'oldprefix123', ?, 'ready', ?)`, now.Add(time.Hour).Format(time.RFC3339Nano), stamp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rotator := &fakeHeadscaleKeyRotator{
		rotation: deployapi.HeadscaleAPIKeyRotation{
			APIKey: fakeHeadscaleAPIKey("newprefix123", "n"), APIKeyID: 2,
			APIKeyPrefix: "newprefix123", APIKeyExpiresAt: now.Add(365 * 24 * time.Hour),
		},
		commitFailures: 1,
	}
	server := NewServer(store, "", false).WithInfrastructureManager(rotator)
	if err := server.MaintainHeadscaleAPIKey(context.Background()); err == nil || !strings.Contains(err.Error(), "response lost") {
		t.Fatalf("commit response loss was not reported: %v", err)
	}
	state, exists, err := store.headscaleAPIKeyState(context.Background())
	if err != nil || !exists || state.State != "committing" || state.KeyPrefix != "newprefix123" || state.PreviousPrefix != "oldprefix123" {
		t.Fatalf("rotation was not recoverable after response loss: state=%#v exists=%v err=%v", state, exists, err)
	}
	if err := server.MaintainHeadscaleAPIKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _, err = store.headscaleAPIKeyState(context.Background())
	if err != nil || state.State != "ready" || state.PreviousPrefix != "" || state.KeyPrefix != "newprefix123" || state.ExpiresAt != now.Add(365*24*time.Hour) {
		t.Fatalf("rotation did not complete: state=%#v err=%v", state, err)
	}
	if len(rotator.prepared) != 1 || len(rotator.committed) != 2 || rotator.committed[1].PreviousPrefix != "oldprefix123" {
		t.Fatalf("unexpected idempotent rotation calls: prepared=%#v committed=%#v", rotator.prepared, rotator.committed)
	}
	_, activeKey, err := store.integrationSecret(context.Background(), "headscale")
	if err != nil || activeKey != rotator.rotation.APIKey {
		t.Fatalf("replacement credential was not active: key_match=%v err=%v", activeKey == rotator.rotation.APIKey, err)
	}
}
