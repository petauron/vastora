package center

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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
	// Construct the protocol credential only from 256 bits of CSPRNG output;
	// no caller-supplied or human-chosen password may reach this digest.
	var entropy [nodediagnostics.LinkBandwidthCredentialBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return client, server, err
	}
	token := base64.RawURLEncoding.EncodeToString(entropy[:])
	// iperf3 requires SHA256("{username}" + token). This is a digest of a
	// random one-time token, not a password-storage KDF. Changing it to Argon2
	// or bcrypt would make the upstream server reject every authenticated test.
	hash := sha256.Sum256([]byte("{meridian}" + token))
	client = nodediagnostics.LinkBandwidthAuth{KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})), Password: token}
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
