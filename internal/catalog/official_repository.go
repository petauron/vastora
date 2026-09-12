package catalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// VerifyOfficialRepository uses exactly the online client's verification path,
// but resolves requests from a staged directory. It never contacts a network.
// The trusted root is independently supplied, not taken from that directory.
func VerifyOfficialRepository(ctx context.Context, directory, channel string, root []byte) (OfficialFetchResult, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return OfficialFetchResult{}, errors.New("catalog: invalid staged repository")
	}
	return fetchOfficial(ctx, "https://catalog.invalid/", channel, root, OfficialFetchState{}, repositoryTransport{directory})
}

type repositoryTransport struct{ directory string }

func (t repositoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.Host != "catalog.invalid" || request.URL.Scheme != "https" || request.URL.RawQuery != "" {
		return nil, errors.New("catalog: unexpected staged repository request")
	}
	relative := strings.TrimPrefix(request.URL.Path, "/")
	if !filepath.IsLocal(relative) || strings.Contains(relative, "\\") {
		return nil, errors.New("catalog: invalid staged repository path")
	}
	location := t.directory
	for _, part := range strings.Split(relative, "/") {
		location = filepath.Join(location, part)
		info, err := os.Lstat(location)
		if os.IsNotExist(err) {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header), Request: request}, nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("catalog: invalid staged repository entry")
		}
	}
	info, err := os.Stat(location)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxEnvelopeBytes {
		return nil, errors.New("catalog: invalid staged repository file")
	}
	raw, err := os.ReadFile(location)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Header: make(http.Header), Request: request}, nil
}
