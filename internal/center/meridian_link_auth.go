package center

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"

	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/secret"
)

// Each probe gets an independent iperf3 credential. Only the landing receives
// the private key; only the entry receives the password. Neither is exposed by
// the diagnostic read API or stored in plaintext in targets_json.
func newMeridianLinkAuth() (client, server nodediagnostics.LinkBandwidthAuth, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return client, server, err
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return client, server, err
	}
	password, err := randomToken(32)
	if err != nil {
		return client, server, err
	}
	hash := sha256.Sum256([]byte("{meridian}" + password))
	client = nodediagnostics.LinkBandwidthAuth{KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})), Password: password}
	server = nodediagnostics.LinkBandwidthAuth{KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), PasswordHash: hex.EncodeToString(hash[:])}
	return client, server, nil
}

type storedMeridianLink struct {
	nodediagnostics.LinkBandwidthTask
	SealedAuth []byte `json:"sealedAuth"`
}

func (s *Store) sealMeridianLink(id string, link nodediagnostics.LinkBandwidthTask, auth nodediagnostics.LinkBandwidthAuth) ([]byte, error) {
	plain, err := json.Marshal(auth)
	if err != nil {
		return nil, err
	}
	sealed, err := secret.Seal(s.key, plain, []byte("meridian-link:"+id))
	if err != nil {
		return nil, err
	}
	return json.Marshal(storedMeridianLink{LinkBandwidthTask: link, SealedAuth: sealed})
}

func (s *Store) openMeridianLinkAuth(id, raw string) (*nodediagnostics.LinkBandwidthAuth, error) {
	var stored storedMeridianLink
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, err
	}
	plain, err := secret.Open(s.key, stored.SealedAuth, []byte("meridian-link:"+id))
	if err != nil {
		return nil, err
	}
	var auth nodediagnostics.LinkBandwidthAuth
	if err := json.Unmarshal(plain, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}
