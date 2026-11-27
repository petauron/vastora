//go:build linux

package dockerruntime

import (
	"errors"
	"math"
	"syscall"
)

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
