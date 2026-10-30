// Package nodesite defines the certificate lifecycle for managed Daylight sites.
package nodesite

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

const Kind = "node.site.certificate"
const TaskPrefix = "node-site-"

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Task struct {
	ApplicationID  string `json:"applicationId"`
	AppKey         string `json:"appKey"`
	Hostname       string `json:"hostname"`
	Port           int    `json:"port"`
	Revision       int64  `json:"revision"`
	Action         string `json:"action"`
	CertificatePEM string `json:"certificatePem,omitempty"`
	PrivateKeyPEM  string `json:"privateKeyPem,omitempty"`
}

type Result struct {
	Action      string `json:"action"`
	Revision    int64  `json:"revision"`
	Hostname    string `json:"hostname"`
	Fingerprint string `json:"fingerprint,omitempty"`
	NotAfter    string `json:"notAfter,omitempty"`
	State       string `json:"state"`
}

func (t Task) Validate() error {
	if t.ApplicationID == "" || len(t.ApplicationID) > 256 || strings.ContainsAny(t.ApplicationID, "\x00\r\n") || (t.AppKey != "vastora-official/daylight" && t.AppKey != "vastora-official/daylight-https") || !ValidHostname(t.Hostname) || t.Revision < 1 || t.Port < 1024 || t.Port > 65535 {
		return errors.New("node site: invalid task identity")
	}
	switch t.Action {
	case "inspect", "rollback":
		if t.CertificatePEM != "" || t.PrivateKeyPEM != "" {
			return errors.New("node site: unexpected certificate")
		}
	case "apply":
		if len(t.CertificatePEM) == 0 || len(t.CertificatePEM) > 65536 || len(t.PrivateKeyPEM) == 0 || len(t.PrivateKeyPEM) > 16384 {
			return errors.New("node site: certificate and private key are required")
		}
	default:
		return errors.New("node site: invalid action")
	}
	return nil
}
func ValidHostname(h string) bool   { return len(h) <= 253 && hostnamePattern.MatchString(h) }
func Fingerprint(der []byte) string { sum := sha256.Sum256(der); return hex.EncodeToString(sum[:]) }
