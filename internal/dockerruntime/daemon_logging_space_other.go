//go:build !linux

package dockerruntime

import "errors"

func filesystemTotalBytes(string) (uint64, error) {
	return 0, errors.New("docker runtime: filesystem size inspection requires Linux")
}
