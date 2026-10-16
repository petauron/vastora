package agent

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

func validateMeridianLinkAuth(auth *nodediagnostics.LinkBandwidthAuth, server bool) error {
	invalid := errors.New("agent: missing or invalid Glivela probe authentication")
	if auth == nil || len(auth.KeyPEM) > 4096 {
		return invalid
	}
	block, rest := pem.Decode([]byte(auth.KeyPEM))
	if block == nil || len(rest) != 0 {
		return invalid
	}
	if server {
		if len(auth.PasswordHash) != hex.EncodedLen(32) || auth.Password != "" {
			return invalid
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		hash, hashErr := hex.DecodeString(auth.PasswordHash)
		if err != nil || key.N.BitLen() != 2048 || key.Validate() != nil || hashErr != nil || len(hash) != 32 {
			return invalid
		}
	} else {
		if len(auth.Password) != base64.RawURLEncoding.EncodedLen(nodediagnostics.LinkBandwidthCredentialBytes) || auth.PasswordHash != "" {
			return invalid
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return invalid
		}
		public, ok := key.(*rsa.PublicKey)
		token, tokenErr := base64.RawURLEncoding.Strict().DecodeString(auth.Password)
		if !ok || public.N.BitLen() != 2048 || tokenErr != nil || len(token) != nodediagnostics.LinkBandwidthCredentialBytes || base64.RawURLEncoding.EncodeToString(token) != auth.Password {
			return invalid
		}
	}
	return nil
}
