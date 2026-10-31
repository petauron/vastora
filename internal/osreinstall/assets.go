package osreinstall

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"
)

// These hashes bind the named source files to the reviewed, signed upstream
// commit. Fetching a verified file alone does not authorize executing it: its
// installation, network and boot behavior must also meet our security policy.
const reinstallCommit = "54fae2067557b3c0ca49e7c9ce03bca63c793f8c"

var reinstallSourceHashes = map[string]string{
	"reinstall.sh":         "3ca358bf065ebd3ecb2f727e152a4b4fb9b154398abc1b9a5fe0b599f1027d7a",
	"trans.sh":             "d8f34529c0a706b17d67965b99a107f8299a30d5303744a20f0fb96556365bf7",
	"initrd-network.sh":    "78fd74716b236d0e41e4f5c4034d45db8bcc47fba2d53c4a19a0d21ef60510bc",
	"ttys.sh":              "758f059c42008e89dc4df44a8da893aad8980765fa2ae06779224e96c00c3b74",
	"get-xda.sh":           "be4193970891e9e1c480b6d8a587f12f27906e65f5486431b88fc0902806e5bc",
	"debian.cfg":           "7a25742bbd03fc73b68716bf1a80f437cbc62bbf9ae30678902a05603c629e06",
	"fix-eth-name.sh":      "4b4505ce0ddc40e90dc12d369460136fa9add6cb2dbea6df280650b9517ebd9f",
	"fix-eth-name.service": "dc667f379b23b13f51f22f8e609bed19e544fcca94b2037faa99bc7f04a90050",
}

const maxReinstallSourceBytes = 2 << 20

// FetchReinstallSource uses only the pinned commit, validates HTTPS, refuses
// cross-host redirects and verifies the entire file before returning its bytes.
func FetchReinstallSource(ctx context.Context, name string) ([]byte, error) {
	expected, ok := reinstallSourceHashes[name]
	if !ok {
		return nil, errors.New("reinstall: unknown installer source")
	}
	roots, err := loadSystemRoots()
	if err != nil {
		return nil, errors.New("reinstall: system HTTPS CA certificates are unavailable")
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Host != "raw.githubusercontent.com" {
				return errors.New("reinstall: installer source redirect rejected")
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()
	url := "https://raw.githubusercontent.com/bin456789/reinstall/" + reinstallCommit + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("reinstall: invalid installer source")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("reinstall: cannot download installer source")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxReinstallSourceBytes {
		return nil, errors.New("reinstall: installer source unavailable or too large")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReinstallSourceBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxReinstallSourceBytes {
		return nil, errors.New("reinstall: invalid installer source length")
	}
	if !sameSHA256(body, expected) {
		return nil, errors.New("reinstall: installer source digest mismatch")
	}
	return body, nil
}

func sameSHA256(body []byte, expected string) bool {
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256(body)
	return subtle.ConstantTimeCompare(got[:], want) == 1
}
