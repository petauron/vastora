package osreinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPinnedSourceIntegrity(t *testing.T) {
	data := []byte("verified installer fixture")
	digest := sha256.Sum256(data)
	expected := hex.EncodeToString(digest[:])
	if !sameSHA256(data, expected) || sameSHA256(append(data, '!'), expected) || sameSHA256(data, "invalid") {
		t.Fatal("source digest verification failed")
	}
	if _, err := FetchReinstallSource(context.Background(), "../reinstall.sh"); err == nil {
		t.Fatal("unknown source accepted")
	}
}
