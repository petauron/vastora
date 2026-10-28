package agent

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"reflect"
	"strings"

	"github.com/moby/moby/client"
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
		if strings.ContainsAny(header.Name, "\\\x00\r\n") || path.IsAbs(header.Name) || strings.TrimSuffix(header.Name, "/") != name || header.Linkname != "" || (name != root && !strings.HasPrefix(name, root+"/")) || header.Size < 0 || len(entries) >= 100000 {
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
			if _, err := io.CopyN(output, senlunContextReader{ctx: ctx, source: reader}, header.Size); err != nil {
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

type senlunContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader senlunContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// transferSenlunVolume streams directly from the stopped original container to
// a newly owned volume. No archive or application backup is written to disk.
func transferSenlunVolume(ctx context.Context, docker packageDockerEngine, sourceID, targetID, mountPath string) error {
	root := path.Base(mountPath)
	if !path.IsAbs(mountPath) || path.Clean(mountPath) != mountPath || root == "." || root == "/" {
		return errors.New("agent: invalid Senlun volume mount")
	}
	original, err := docker.CopyFromContainer(ctx, sourceID, client.CopyFromContainerOptions{SourcePath: mountPath})
	if err != nil || original.Content == nil {
		return errors.New("agent: original Senlun data cannot be read")
	}
	defer original.Content.Close()
	reader, writer := io.Pipe()
	type streamed struct {
		entries map[string]senlunVolumeEntry
		err     error
	}
	finished := make(chan streamed, 1)
	go func() {
		entries, copyErr := streamSenlunVolume(ctx, original.Content, writer, root)
		_ = writer.CloseWithError(copyErr)
		finished <- streamed{entries: entries, err: copyErr}
	}()
	_, copyErr := docker.CopyToContainer(ctx, targetID, client.CopyToContainerOptions{DestinationPath: path.Dir(mountPath), Content: reader})
	if copyErr != nil {
		_ = reader.CloseWithError(copyErr)
	} else {
		_ = reader.Close()
	}
	before := <-finished
	if copyErr != nil || before.err != nil {
		return errors.New("agent: direct Senlun volume transfer failed")
	}
	copied, err := docker.CopyFromContainer(ctx, targetID, client.CopyFromContainerOptions{SourcePath: mountPath})
	if err != nil || copied.Content == nil {
		return errors.New("agent: transferred Senlun data cannot be verified")
	}
	after, verifyErr := streamSenlunVolume(ctx, copied.Content, nil, root)
	closeErr := copied.Content.Close()
	if verifyErr != nil || closeErr != nil || !reflect.DeepEqual(before.entries, after) {
		return errors.New("agent: transferred Senlun data or ownership differs")
	}
	return nil
}
