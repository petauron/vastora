package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/petauron/catalog/catalog"
)

// A root/operator may deliver the exact signed archive over another channel.
// This is input data, never an ownership receipt or an executable installer.
func readNativePackageArchive(ctx context.Context, client *http.Client, stateDirectory, applicationID string, artifact catalog.Artifact) ([]byte, error) {
	decoded, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != artifact.SHA256 {
		return nil, errors.New("agent: invalid native archive digest")
	}
	path := filepath.Join(stateDirectory, "packages", packageIdentity(applicationID), "incoming", artifact.SHA256)
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return catalog.DownloadArtifact(ctx, client, artifact)
	}
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(artifact.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(artifact.URL, "?#") {
		return nil, errors.New("agent: invalid native archive URL")
	}
	private := func(info os.FileInfo) bool {
		owner, ok := info.Sys().(*syscall.Stat_t)
		return ok && int(owner.Uid) == os.Geteuid() && info.Mode().Perm()&0077 == 0
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || !directory.IsDir() || !private(directory) || !info.Mode().IsRegular() || !private(info) || info.Mode().Perm()&0100 != 0 || info.Size() <= 0 || info.Size() > catalog.MaxArtifactBytes {
		return nil, errors.New("agent: unsafe staged native archive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("agent: staged native archive changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, catalog.MaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if int64(len(raw)) != info.Size() || hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errors.New("agent: staged native archive SHA256 mismatch")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}
