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
	"os"
	"path/filepath"
	"time"
)

type debianInstallerAsset struct {
	url      string
	sha256   string
	maxBytes int64
}

// Digests were checked against Debian's signed trixie InRelease, via the
// corresponding SHA256SUMS and signed udeb Packages index. This immutable
// installer is deliberately not selected by a mutable "current" URL.
var debianInstallerAssets = map[string]debianInstallerAsset{
	"amd64:linux":  {"https://deb.debian.org/debian/dists/trixie/main/installer-amd64/20250803%2Bdeb13u7/images/netboot/debian-installer/amd64/linux", "2b2358b37674d2505350528875bb17afae2a36522a9e8a9417eaca65a7da0e08", 32 << 20},
	"amd64:initrd": {"https://deb.debian.org/debian/dists/trixie/main/installer-amd64/20250803%2Bdeb13u7/images/netboot/debian-installer/amd64/initrd.gz", "57303d157cffce3fa402301667fbc9b2280aa7372082b081e777375ae49da5f2", 64 << 20},
	"amd64:fdisk":  {"https://deb.debian.org/debian/pool/main/u/util-linux/fdisk-udeb_2.41.5-0%2Bdeb13u1_amd64.udeb", "b1c87818fe40d6b6b0b5e49285fecac9dda5fdd142ca9a7a656c7bef8beec59d", 4 << 20},
	"arm64:linux":  {"https://deb.debian.org/debian/dists/trixie/main/installer-arm64/20250803%2Bdeb13u7/images/netboot/debian-installer/arm64/linux", "9c37ace54ef7e1919411fd912e9f34d6982ddd7f4c8ef3c8aee56f4c4d70db67", 32 << 20},
	"arm64:initrd": {"https://deb.debian.org/debian/dists/trixie/main/installer-arm64/20250803%2Bdeb13u7/images/netboot/debian-installer/arm64/initrd.gz", "f8f48fe31f84fca1a43c3d57c09cd1c2ee728549dd18a986463efd0305249451", 64 << 20},
	"arm64:fdisk":  {"https://deb.debian.org/debian/pool/main/u/util-linux/fdisk-udeb_2.41.5-0%2Bdeb13u1_arm64.udeb", "625acebc645c1f3b66381e1df6b4a25b6ea742ffaf9af8a70eba583962169071", 4 << 20},
}

// DownloadDebianInstallerAsset streams to a private staging file and exposes
// the path only after the complete response and its pinned digest are verified.
// The caller owns the private directory and removes it when the operation ends.
func DownloadDebianInstallerAsset(ctx context.Context, arch, kind, dir string) (string, error) {
	spec, ok := debianInstallerAssets[arch+":"+kind]
	if !ok {
		return "", errors.New("reinstall: unsupported Debian installer artifact")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("reinstall: installer staging directory is not private")
	}
	roots, err := loadSystemRoots()
	if err != nil {
		return "", errors.New("reinstall: system HTTPS CA certificates are unavailable")
	}
	client := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Host != "deb.debian.org" {
				return errors.New("reinstall: installer artifact redirect rejected")
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.url, nil)
	if err != nil {
		return "", errors.New("reinstall: invalid installer artifact")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("reinstall: cannot download Debian installer")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > spec.maxBytes {
		return "", errors.New("reinstall: Debian installer artifact unavailable or too large")
	}
	file, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", errors.New("reinstall: cannot create installer staging file")
	}
	temp := file.Name()
	defer os.Remove(temp)
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, spec.maxBytes+1))
	if err != nil || size == 0 || size > spec.maxBytes || resp.ContentLength >= 0 && size != resp.ContentLength {
		return "", errors.New("reinstall: Debian installer artifact is truncated or too large")
	}
	want, err := hex.DecodeString(spec.sha256)
	if err != nil || len(want) != sha256.Size || subtle.ConstantTimeCompare(hash.Sum(nil), want) != 1 {
		return "", errors.New("reinstall: Debian installer artifact digest mismatch")
	}
	if err := file.Sync(); err != nil {
		return "", errors.New("reinstall: cannot sync installer artifact")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("reinstall: cannot close installer artifact")
	}
	// Link is atomic and refuses to replace an earlier file or symlink.
	final := filepath.Join(dir, "debian13-"+arch+"-"+kind)
	if err := os.Link(temp, final); err != nil {
		return "", errors.New("reinstall: installer artifact already exists or cannot be published")
	}
	staging, err := os.Open(dir)
	if err != nil {
		_ = os.Remove(final)
		return "", errors.New("reinstall: cannot sync installer staging directory")
	}
	err = staging.Sync()
	_ = staging.Close()
	if err != nil {
		_ = os.Remove(final)
		return "", errors.New("reinstall: cannot sync installer staging directory")
	}
	return final, nil
}
