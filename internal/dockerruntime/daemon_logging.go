package dockerruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultDaemonConfigPath is the standard Docker daemon configuration file.
	DefaultDaemonConfigPath = "/etc/docker/daemon.json"

	// DefaultDataRoot is Docker's default data directory when neither
	// "data-root" nor legacy "graph" is configured in daemon.json.
	DefaultDataRoot = "/var/lib/docker"

	// LargeDiskThresholdBytes selects between the two per-container rotation
	// defaults (100m x 5 below 15 GiB or when the Docker data filesystem cannot
	// be confirmed; 200m x 5 at or above 15 GiB). Docker's json-file driver
	// enforces per-container rotation only, not a daemon-wide disk quota.
	LargeDiskThresholdBytes uint64 = 15 << 30

	// DefaultSmallDiskLogMaxSize is the conservative per-container log file size
	// limit used below 15 GiB or whenever the Docker data filesystem cannot be
	// reliably confirmed.
	DefaultSmallDiskLogMaxSize = "100m"

	// DefaultLargeDiskLogMaxSize is the per-container log file size limit used
	// when the verified Docker data filesystem is at least 15 GiB.
	DefaultLargeDiskLogMaxSize = "200m"

	// DefaultLogMaxFile is the maximum number of rotated log files per container.
	DefaultLogMaxFile = "5"

	// DefaultLogCompress enables gzip compression for rotated json-file logs.
	DefaultLogCompress = "true"

	maxDaemonConfigBytes = 1 << 20
)

// DaemonLoggingAction describes the outcome of EnsureDefaultDaemonLogging.
type DaemonLoggingAction string

const (
	DaemonLoggingCreated   DaemonLoggingAction = "created"
	DaemonLoggingUpdated   DaemonLoggingAction = "updated"
	DaemonLoggingPreserved DaemonLoggingAction = "preserved"
)

// DaemonLoggingResult reports the action taken on daemon.json.
type DaemonLoggingResult struct {
	Action            DaemonLoggingAction
	Path              string
	MaxSize           string
	MaxFile           string
	Compress          string
	DataRootConfirmed bool
}

// DaemonLoggingOptions configures EnsureDefaultDaemonLogging. Zero values use
// the standard Linux host paths and filesystem inspection.
type DaemonLoggingOptions struct {
	ConfigPath      string
	DefaultDataRoot string
	FilesystemBytes func(path string) (uint64, error)
}

// EnsureDefaultDaemonLogging writes bounded json-file log rotation defaults to
// /etc/docker/daemon.json before a fresh Docker daemon starts. It preserves
// existing log-driver or log-opts settings, retains unknown fields and existing
// file permissions, refuses invalid or non-object JSON and symlinks, and writes
// updates atomically. It never restarts a running Docker daemon.
func EnsureDefaultDaemonLogging(options DaemonLoggingOptions) (DaemonLoggingResult, error) {
	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		configPath = DefaultDaemonConfigPath
	}
	if !filepath.IsAbs(configPath) {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: daemon configuration path must be absolute: %q", configPath)
	}
	configPath = filepath.Clean(configPath)

	defaultDataRoot := strings.TrimSpace(options.DefaultDataRoot)
	if defaultDataRoot == "" {
		defaultDataRoot = DefaultDataRoot
	}
	defaultDataRoot = filepath.Clean(defaultDataRoot)

	fsBytes := options.FilesystemBytes
	if fsBytes == nil {
		fsBytes = filesystemTotalBytes
	}

	dir := filepath.Dir(configPath)
	if err := inspectOptionalDirectory(dir); err != nil {
		return DaemonLoggingResult{}, err
	}

	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		maxSize, confirmed := selectDefaultLogMaxSize(nil, defaultDataRoot, fsBytes)
		doc := make(map[string]json.RawMessage, 2)
		if err := setDefaultLogConfig(doc, maxSize); err != nil {
			return DaemonLoggingResult{}, err
		}
		payload, err := encodeDaemonConfig(doc)
		if err != nil {
			return DaemonLoggingResult{}, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return DaemonLoggingResult{}, fmt.Errorf("docker runtime: create %s: %w", dir, err)
		}
		if err := inspectRequiredDirectory(dir); err != nil {
			return DaemonLoggingResult{}, err
		}
		if err := writeDaemonConfigAtomic(configPath, payload, 0o644); err != nil {
			return DaemonLoggingResult{}, err
		}
		return DaemonLoggingResult{
			Action:            DaemonLoggingCreated,
			Path:              configPath,
			MaxSize:           maxSize,
			MaxFile:           DefaultLogMaxFile,
			Compress:          DefaultLogCompress,
			DataRootConfirmed: confirmed,
		}, nil
	}
	if err != nil {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: inspect %s: %w", configPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: refusing to modify symbolic link %s", configPath)
	}
	if !info.Mode().IsRegular() {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: refusing to modify non-regular file %s", configPath)
	}
	if info.Size() > maxDaemonConfigBytes {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: refusing to modify %s exceeding %d bytes", configPath, maxDaemonConfigBytes)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: read %s: %w", configPath, err)
	}
	doc, err := parseDaemonConfigObject(raw)
	if err != nil {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: %s: %w", configPath, err)
	}
	if _, hasDriver := doc["log-driver"]; hasDriver {
		return DaemonLoggingResult{Action: DaemonLoggingPreserved, Path: configPath}, nil
	}
	if _, hasOpts := doc["log-opts"]; hasOpts {
		return DaemonLoggingResult{Action: DaemonLoggingPreserved, Path: configPath}, nil
	}

	maxSize, confirmed := selectDefaultLogMaxSize(doc, defaultDataRoot, fsBytes)
	if err := setDefaultLogConfig(doc, maxSize); err != nil {
		return DaemonLoggingResult{}, err
	}
	payload, err := encodeDaemonConfig(doc)
	if err != nil {
		return DaemonLoggingResult{}, err
	}
	if err := writeDaemonConfigAtomic(configPath, payload, info.Mode().Perm()); err != nil {
		return DaemonLoggingResult{}, err
	}
	return DaemonLoggingResult{
		Action:            DaemonLoggingUpdated,
		Path:              configPath,
		MaxSize:           maxSize,
		MaxFile:           DefaultLogMaxFile,
		Compress:          DefaultLogCompress,
		DataRootConfirmed: confirmed,
	}, nil
}

func inspectOptionalDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("docker runtime: inspect %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("docker runtime: refusing to modify configuration under symbolic link %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("docker runtime: %s is not a directory", dir)
	}
	return nil
}

func inspectRequiredDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("docker runtime: inspect %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("docker runtime: %s is not a real directory", dir)
	}
	return nil
}

func parseDaemonConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("daemon configuration must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var doc map[string]json.RawMessage
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if doc == nil {
		return nil, errors.New("daemon configuration must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("daemon configuration contains trailing data after JSON object")
	}
	return doc, nil
}

func selectDefaultLogMaxSize(doc map[string]json.RawMessage, defaultDataRoot string, fsBytes func(string) (uint64, error)) (string, bool) {
	if doc != nil {
		rawDataRoot, hasDataRoot := doc["data-root"]
		rawGraph, hasGraph := doc["graph"]
		if hasDataRoot && hasGraph {
			return DefaultSmallDiskLogMaxSize, false
		}
		if hasDataRoot || hasGraph {
			rawValue := rawDataRoot
			if !hasDataRoot {
				rawValue = rawGraph
			}
			configured, ok := parseAbsoluteDataRoot(rawValue)
			if !ok {
				return DefaultSmallDiskLogMaxSize, false
			}
			info, err := os.Stat(configured)
			if err != nil || !info.IsDir() {
				return DefaultSmallDiskLogMaxSize, false
			}
			return maxSizeForPath(configured, fsBytes)
		}
	}
	probe, ok := nearestExistingDirectory(defaultDataRoot)
	if !ok {
		return DefaultSmallDiskLogMaxSize, false
	}
	return maxSizeForPath(probe, fsBytes)
}

func parseAbsoluteDataRoot(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", false
	}
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	return filepath.Clean(value), true
}

func nearestExistingDirectory(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	probe := filepath.Clean(path)
	for {
		info, err := os.Stat(probe)
		if err == nil {
			if info.IsDir() {
				return probe, true
			}
			return "", false
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", false
		}
		probe = parent
	}
}

func maxSizeForPath(path string, fsBytes func(string) (uint64, error)) (string, bool) {
	total, err := fsBytes(path)
	if err != nil || total == 0 {
		return DefaultSmallDiskLogMaxSize, false
	}
	if total >= LargeDiskThresholdBytes {
		return DefaultLargeDiskLogMaxSize, true
	}
	return DefaultSmallDiskLogMaxSize, true
}

func setDefaultLogConfig(doc map[string]json.RawMessage, maxSize string) error {
	driverRaw, err := json.Marshal("json-file")
	if err != nil {
		return err
	}
	optsRaw, err := json.Marshal(map[string]string{
		"compress": DefaultLogCompress,
		"max-file": DefaultLogMaxFile,
		"max-size": maxSize,
	})
	if err != nil {
		return err
	}
	doc["log-driver"] = driverRaw
	doc["log-opts"] = optsRaw
	return nil
}

func encodeDaemonConfig(doc map[string]json.RawMessage) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("docker runtime: encode daemon configuration: %w", err)
	}
	return buffer.Bytes(), nil
}

func writeDaemonConfigAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".daemon.json.vastora-*")
	if err != nil {
		return fmt.Errorf("docker runtime: stage %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("docker runtime: set permissions on %s: %w", path, err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("docker runtime: write %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("docker runtime: sync %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("docker runtime: close %s: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("docker runtime: replace %s: %w", path, err)
	}
	keep = true
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("docker runtime: open %s: %w", dir, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("docker runtime: sync %s: %w", dir, err)
	}
	return nil
}
