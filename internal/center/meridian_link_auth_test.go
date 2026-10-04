package center

import (
	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

func TestMeridianLinkAuthIsSeparateEncryptedAndTaskBound(t *testing.T) {
	client, server, err := newMeridianLinkAuth()
	if err != nil {
		t.Fatal(err)
	}
	pubBlock, _ := pem.Decode([]byte(client.KeyPEM))
	privateBlock, _ := pem.Decode([]byte(server.KeyPEM))
	pub, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.ParsePKCS1PrivateKey(privateBlock.Bytes)
	if err != nil || !pub.(*rsa.PublicKey).Equal(&private.PublicKey) {
		t.Fatal("invalid iperf RSA pair")
	}
	hash := sha256.Sum256([]byte("{meridian}" + client.Password))
	if server.PasswordHash != hex.EncodeToString(hash[:]) || server.Password != "" || client.PasswordHash != "" {
		t.Fatal("iperf credentials crossed roles")
	}
	store := openOrchestrationStore(t)
	defer store.Close()
	for _, auth := range []nodediagnostics.LinkBandwidthAuth{client, server} {
		stored, err := store.sealMeridianLink("probe", nodediagnostics.LinkBandwidthTask{}, auth)
		if err != nil {
			t.Fatal(err)
		}
		plain, _ := json.Marshal(auth)
		if bytes.Contains(stored, plain) || bytes.Contains(stored, []byte(client.Password)) || bytes.Contains(stored, []byte("PRIVATE KEY")) {
			t.Fatal("plaintext authentication persisted")
		}
		opened, err := store.openMeridianLinkAuth("probe", string(stored))
		if err != nil || *opened != auth {
			t.Fatalf("auth readback failed: %v", err)
		}
		if _, err := store.openMeridianLinkAuth("other-probe", string(stored)); err == nil {
			t.Fatal("ciphertext replay accepted for another task")
		}
	}
}
