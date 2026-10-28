package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"strings"
)

// senlunVolumeEntry captures the data and ownership that must survive a direct
// volume transfer. It contains no application data or credential contents.
type senlunVolumeEntry struct {
	Directory bool
	Mode      int64
	UID       int
	GID       int
	Size      int64
	SHA256    string
}

// streamSenlunVolume checks a Docker volume archive as it streams. It rejects
// paths, links and special files that cannot be safely copied into the new
// volume. A nil destination performs only the same bounded verification.
func streamSenlunVolume(ctx context.Context, source io.Reader, destination io.Writer, root string) (map[string]senlunVolumeEntry, error) {
	if root == "" || path.Base(root) != root || root == "." || root == ".." {
		return nil, errors.New("agent: invalid Senlun data root")
	}
	reader := tar.NewReader(source)
	var writer *tar.Writer
	if destination != nil {
		writer = tar.NewWriter(destination)
	}
	entries := make(map[string]senlunVolumeEntry)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("agent: Senlun data archive is unreadable")
		}
		name := path.Clean(header.Name)
		if strings.ContainsAny(header.Name, "\\\x00\r\n") || path.IsAbs(header.Name) || (name != root && !strings.HasPrefix(name, root+"/")) || header.Size < 0 || len(entries) >= 100000 {
			return nil, errors.New("agent: unsafe Senlun data archive path")
		}
		if _, exists := entries[name]; exists {
			return nil, errors.New("agent: duplicate Senlun data archive path")
		}
		entry := senlunVolumeEntry{Mode: header.Mode & 07777, UID: header.Uid, GID: header.Gid, Size: header.Size}
		switch header.Typeflag {
		case tar.TypeDir:
			entry.Directory = true
			if header.Size != 0 {
				return nil, errors.New("agent: invalid Senlun data directory")
			}
		case tar.TypeReg, tar.TypeRegA:
			size += header.Size
			if size < 0 || size > 64<<30 {
				return nil, errors.New("agent: Senlun data transfer exceeds its limit")
			}
		default:
			return nil, errors.New("agent: Senlun data contains unsupported links or special files")
		}
		if writer != nil {
			if err := writer.WriteHeader(header); err != nil {
				return nil, err
			}
		}
		if !entry.Directory {
			digest := sha256.New()
			output := io.Writer(digest)
			if writer != nil {
				output = io.MultiWriter(digest, writer)
			}
			if _, err := io.CopyN(output, reader, header.Size); err != nil {
				return nil, errors.New("agent: Senlun data file could not be transferred")
			}
			entry.SHA256 = hex.EncodeToString(digest.Sum(nil))
		}
		entries[name] = entry
	}
	if len(entries) == 0 {
		return nil, errors.New("agent: Senlun data archive is empty")
	}
	if writer != nil {
		if err := writer.Close(); err != nil {
			return nil, err
		}
	}
	return entries, nil
}
