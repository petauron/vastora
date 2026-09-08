package recovery

import "golang.org/x/sys/unix"

func renameExclusive(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
