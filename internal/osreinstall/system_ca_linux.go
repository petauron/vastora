//go:build linux

package osreinstall

import (
	"crypto/x509"
	"errors"
	"io"
	"os"
	"syscall"
)

// loadSystemRoots reads the current Debian/Ubuntu CA bundle for each
// inspection or download. Go's cached system pool otherwise stays empty when
// ca-certificates is installed after the Agent starts.
func loadSystemRoots() (*x509.CertPool, error) {
	const path = "/etc/ssl/certs/ca-certificates.crt"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > 4<<20 {
		return nil, errors.New("reinstall: system HTTPS CA certificates are unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, errors.New("reinstall: system HTTPS CA certificates are unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) {
		return nil, errors.New("reinstall: system HTTPS CA certificates changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	last, statErr := file.Stat()
	if err != nil || statErr != nil || int64(len(data)) != info.Size() || last.Size() != info.Size() || !last.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("reinstall: system HTTPS CA certificates changed")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("reinstall: system HTTPS CA certificates are invalid")
	}
	return pool, nil
}
