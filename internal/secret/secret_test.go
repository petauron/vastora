package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealRoundTripAndTamperDetection(t *testing.T) {
	t.Parallel()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, []byte("private value"), []byte("record:1"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, sealed, []byte("record:1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, []byte("private value")) {
		t.Fatalf("unexpected plaintext %q", plain)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := Open(key, sealed, []byte("record:1")); err == nil {
		t.Fatal("expected tamper detection")
	}
}

func TestLoadOrCreateKeyRefusesBroadPermissions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "root.key")
	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != KeySize {
		t.Fatalf("got key length %d", len(key))
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); !errors.Is(err, ErrUnsafeMode) {
		t.Fatalf("got %v, want ErrUnsafeMode", err)
	}
}
