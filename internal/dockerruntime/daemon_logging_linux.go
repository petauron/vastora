//go:build linux

package dockerruntime

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"syscall"
)

type fileIdentity struct {
	dev  uint64
	ino  uint64
	mode os.FileMode
}

func filesystemTotalBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blockSize := stat.Frsize
	if blockSize <= 0 {
		blockSize = stat.Bsize
	}
	if blockSize <= 0 || stat.Blocks == 0 {
		return 0, errors.New("docker runtime: invalid filesystem size")
	}
	unit := uint64(blockSize)
	blocks := uint64(stat.Blocks)
	if blocks > math.MaxUint64/unit {
		return math.MaxUint64, nil
	}
	return blocks * unit, nil
}

func readRegularNoFollow(path string, maxBytes int64) (fileIdentity, []byte, bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileIdentity{}, nil, false, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify symbolic link %s", path)
		}
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: open %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify non-regular file %s", path)
	}
	if info.Size() > maxBytes {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify %s exceeding %d bytes", path, maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: read %s: %w", path, err)
	}
	if int64(len(raw)) > maxBytes {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: refusing to modify %s exceeding %d bytes", path, maxBytes)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, nil, false, fmt.Errorf("docker runtime: inspect identity of %s", path)
	}
	return fileIdentity{
		dev:  uint64(stat.Dev),
		ino:  stat.Ino,
		mode: info.Mode().Perm(),
	}, raw, true, nil
}
