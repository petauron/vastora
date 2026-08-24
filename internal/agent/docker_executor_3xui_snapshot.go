package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

func snapshotThreeXUIDatabase(ctx context.Context, docker threeXUIContainerEngine, containerID string) ([]byte, error) {
	archive, err := docker.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: "/etc/x-ui"})
	if err != nil {
		return nil, err
	}
	defer archive.Content.Close()
	data, err := readThreeXUISnapshotArchive(archive.Content)
	if err != nil {
		return nil, err
	}
	return normalizeThreeXUIDatabaseSnapshot(data)
}

func readThreeXUISnapshotArchive(reader io.Reader) ([]byte, error) {
	const maxSnapshotBytes = 128 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, errors.New("3x-ui database snapshot exceeds 128 MiB")
	}
	return data, nil
}

func normalizeThreeXUIDatabaseSnapshot(data []byte) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(data))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	databaseFound := false
	fileFound := false
	journalNames := map[string]bool{
		"x-ui/x-ui.db-journal": false,
		"x-ui/x-ui.db-wal":     false,
		"x-ui/x-ui.db-shm":     false,
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read 3x-ui database snapshot: %w", err)
		}
		name, err := safeThreeXUISnapshotPath(header.Name)
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return nil, fmt.Errorf("3x-ui database snapshot contains unsupported entry %q", name)
		}
		if name == "x-ui/x-ui.db" {
			databaseFound = true
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			fileFound = true
		}
		if _, ok := journalNames[name]; ok {
			journalNames[name] = true
		}
		copyHeader := *header
		copyHeader.Name = name
		copyHeader.Linkname = ""
		copyHeader.PAXRecords = nil
		if err := writer.WriteHeader(&copyHeader); err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			return nil, err
		}
	}
	if !databaseFound {
		if !fileFound {
			return nil, errThreeXUIVolumeEmpty
		}
		return nil, errThreeXUIDatabaseMissing
	}
	for name, found := range journalNames {
		if found {
			continue
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 0, ModTime: time.Now().UTC()}); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func safeThreeXUISnapshotPath(value string) (string, error) {
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "etc/")
	cleaned := path.Clean(value)
	if value == "" || cleaned == "." || path.IsAbs(value) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || (cleaned != "x-ui" && !strings.HasPrefix(cleaned, "x-ui/")) {
		return "", fmt.Errorf("3x-ui database snapshot contains unsafe path %q", value)
	}
	return cleaned, nil
}

func restoreThreeXUIDatabase(ctx context.Context, docker threeXUIContainerEngine, containerID string, snapshot []byte) error {
	if len(snapshot) == 0 {
		return errors.New("3x-ui database snapshot is empty")
	}
	_, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath: "/etc", Content: bytes.NewReader(snapshot), AllowOverwriteDirWithFile: false, CopyUIDGID: true,
	})
	return err
}

func persistThreeXUIDatabaseSnapshot(ctx context.Context, docker threeXUIContainerEngine, containerID string, snapshot []byte) error {
	archive, err := relocateThreeXUIDatabaseSnapshot(snapshot, strings.TrimPrefix(threeXUIDurableSnapshot, "/"))
	if err != nil {
		return err
	}
	_, err = docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath: "/", Content: bytes.NewReader(archive), AllowOverwriteDirWithFile: false, CopyUIDGID: true,
	})
	return err
}

func relocateThreeXUIDatabaseSnapshot(snapshot []byte, root string) ([]byte, error) {
	if len(snapshot) == 0 || strings.TrimSpace(root) == "" || strings.Contains(root, "..") {
		return nil, errors.New("invalid durable 3x-ui database snapshot")
	}
	reader := tar.NewReader(bytes.NewReader(snapshot))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	if err := writer.WriteHeader(&tar.Header{Name: root, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Now().UTC()}); err != nil {
		return nil, err
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name, err := safeThreeXUISnapshotPath(header.Name)
		if err != nil {
			return nil, err
		}
		copyHeader := *header
		copyHeader.Name = path.Join(root, name)
		copyHeader.PAXRecords = nil
		if err := writer.WriteHeader(&copyHeader); err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func loadDurableThreeXUIDatabaseSnapshot(ctx context.Context, docker threeXUIContainerEngine, containerID string) ([]byte, error) {
	archive, err := docker.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: threeXUIDurableSnapshot + "/x-ui"})
	if err != nil {
		return nil, err
	}
	defer archive.Content.Close()
	data, err := readThreeXUISnapshotArchive(archive.Content)
	if err != nil {
		return nil, err
	}
	return normalizeThreeXUIDatabaseSnapshot(data)
}
