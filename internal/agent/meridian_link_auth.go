package agent

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

func validateMeridianLinkAuth(auth *nodediagnostics.LinkBandwidthAuth, server bool) error {
	invalid := errors.New("agent: missing or invalid Meridian probe authentication")
	if auth == nil || len(auth.KeyPEM) > 4096 {
		return invalid
	}
	block, rest := pem.Decode([]byte(auth.KeyPEM))
	if block == nil || len(rest) != 0 {
		return invalid
	}
	if server {
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		hash, hashErr := hex.DecodeString(auth.PasswordHash)
		if err != nil || key.N.BitLen() != 2048 || key.Validate() != nil || hashErr != nil || len(hash) != 32 || auth.Password != "" {
			return invalid
		}
	} else {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return invalid
		}
		public, ok := key.(*rsa.PublicKey)
		if !ok || public.N.BitLen() != 2048 || len(auth.Password) < 32 || len(auth.Password) > 128 || auth.PasswordHash != "" {
			return invalid
		}
	}
	return nil
}
