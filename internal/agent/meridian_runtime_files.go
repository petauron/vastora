package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"github.com/petauron/glivela"
)

// Immutable files are installed before the atomic config rename. An older
// config continues referring to its own files while its successor is staged.
func (s *Store) stageMeridianFiles(artifact meridian.DesiredArtifact, uid int) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	directory := filepath.Join(s.dataDir, meridianRuntimeDirectory)
	for name, contents := range artifact.Files {
		filename := filepath.Join(directory, name)
		existing, err := os.ReadFile(filename)
		if err == nil {
			if !bytes.Equal(existing, contents) {
				return errors.New("agent: runtime script differs from its authorized digest")
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			err = file.Chown(uid, -1)
		}
		if err == nil {
			_, err = file.Write(contents)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			_ = os.Remove(filename)
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return s.verifyMeridianFiles(artifact)
}

func (s *Store) verifyMeridianFiles(artifact meridian.DesiredArtifact) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	for name, contents := range artifact.Files {
		filename := filepath.Join(s.dataDir, meridianRuntimeDirectory, name)
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return errors.New("agent: managed runtime script is missing or writable")
		}
		actual, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(actual, contents) {
			return errors.New("agent: managed runtime script digest changed")
		}
	}
	return nil
}
