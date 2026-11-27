//go:build !linux

package dockerruntime

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type fileIdentity struct {
	size    int64
	modNano int64
	mode    os.FileMode
}

func filesystemTotalBytes(string) (uint64, error) {
	return 0, errors.New("docker runtime: filesystem size inspection requires Linux")
}

func readRegularNoFollow(path string, maxBytes int64) (fileIdentity, []byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileIdentity{}, nil, false, nil
		}
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify symbolic link %s", path)
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify non-regular file %s", path)
	}
	if info.Size() > maxBytes {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify %s exceeding %d bytes", path, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: open %s: %w", path, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: read %s: %w", path, err)
	}
	if int64(len(raw)) > maxBytes {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify %s exceeding %d bytes", path, maxBytes)
	}
	return fileIdentity{
		size:    info.Size(),
		modNano: info.ModTime().UnixNano(),
		mode:    info.Mode().Perm(),
	}, raw, true, nil
}
