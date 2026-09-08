//go:build !linux && !darwin

package recovery

import "errors"

func renameExclusive(_, _ string) error {
	return errors.New("recovery: atomic exclusive restore is unsupported on this platform")
}
