package dockerruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultDaemonConfigPath is the standard Docker daemon configuration file.
	DefaultDaemonConfigPath = "/etc/docker/daemon.json"

	// DefaultDataRoot is Docker's default data directory when neither
	// "data-root" nor legacy "graph" is configured in daemon.json.
	DefaultDataRoot = "/var/lib/docker"

	// DefaultMinLogMaxSizeMiB is the lower clamp for confirmed data filesystems.
	DefaultMinLogMaxSizeMiB uint64 = 1

	// DefaultMaxLogMaxSizeMiB is the conservative upper clamp per rotated log
	// file for confirmed data filesystems.
	DefaultMaxLogMaxSizeMiB uint64 = 200

	// DefaultFallbackLogMaxSize is the conservative per-file rotation default
	// used when the Docker data filesystem cannot be reliably confirmed.
	DefaultFallbackLogMaxSize = "50m"

	// DefaultLogMaxFile is the maximum number of rotated log files per container.
	DefaultLogMaxFile = "5"

	// DefaultLogCompress enables gzip compression for rotated json-file logs.
	DefaultLogCompress = "true"

	// Sizing divides 10% of the verified Docker data filesystem across a
	// two-resident-container, five-file planning assumption (10% / 2 / 5 = 1%),
	// floored to whole MiB and clamped to [1m, 200m]. Docker's json-file driver
	// enforces per-container rotation only, not a daemon-wide disk quota.
	defaultLogBudgetDivisor uint64 = 100

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
// existing log-driver or log-opts settings, retains unknown fields and original
// value bytes, rejects invalid or non-object JSON and symlinks, creates missing
// files without clobbering concurrent writers, and verifies existing files did
// not change before replacing them. It never restarts a running Docker daemon.
func EnsureDefaultDaemonLogging(options DaemonLoggingOptions) (DaemonLoggingResult, error) {
	return ensureDefaultDaemonLogging(options, nil)
}

func ensureDefaultDaemonLogging(options DaemonLoggingOptions, beforeCommit func()) (DaemonLoggingResult, error) {
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

	origIdentity, raw, exists, err := readRegularNoFollow(configPath, maxDaemonConfigBytes)
	if err != nil {
		return DaemonLoggingResult{}, err
	}

	if !exists {
		maxSize, confirmed := selectDefaultLogMaxSize(nil, defaultDataRoot, fsBytes)
		payload := formatFreshDaemonConfig(maxSize)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return DaemonLoggingResult{}, fmt.Errorf("docker runtime: create %s: %w", dir, err)
		}
		if err := inspectRequiredDirectory(dir); err != nil {
			return DaemonLoggingResult{}, err
		}
		if err := createDaemonConfigExclusive(configPath, payload, 0o644, beforeCommit); err != nil {
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

	doc, err := parseStrictDaemonConfigObject(raw)
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
	payload, err := appendDefaultLogFields(raw, len(doc) == 0, maxSize)
	if err != nil {
		return DaemonLoggingResult{}, fmt.Errorf("docker runtime: %s: %w", configPath, err)
	}
	if err := replaceDaemonConfigIfUnchanged(configPath, origIdentity, raw, payload, beforeCommit); err != nil {
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

func isASCIIJSONWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func trimASCIIJSONWhitespace(raw []byte) []byte {
	start := 0
	for start < len(raw) && isASCIIJSONWhitespace(raw[start]) {
		start++
	}
	end := len(raw)
	for end > start && isASCIIJSONWhitespace(raw[end-1]) {
		end--
	}
	return raw[start:end]
}

func parseStrictDaemonConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("daemon configuration contains invalid UTF-8")
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid JSON")
	}
	trimmed := trimASCIIJSONWhitespace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("daemon configuration must be a JSON object")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if doc == nil {
		return nil, errors.New("daemon configuration must be a JSON object")
	}
	return doc, nil
}

func formatFreshDaemonConfig(maxSize string) []byte {
	return []byte(fmt.Sprintf("{\n  \"log-driver\": \"json-file\",\n  \"log-opts\": {\n    \"compress\": %q,\n    \"max-file\": %q,\n    \"max-size\": %q\n  }\n}\n", DefaultLogCompress, DefaultLogMaxFile, maxSize))
}

func appendDefaultLogFields(raw []byte, emptyObject bool, maxSize string) ([]byte, error) {
	if emptyObject {
		return formatFreshDaemonConfig(maxSize), nil
	}
	trimmed := trimASCIIJSONWhitespace(raw)
	if len(trimmed) < 2 || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("daemon configuration must be a JSON object")
	}
	prefix := trimASCIIJSONWhitespace(trimmed[:len(trimmed)-1])
	var buffer bytes.Buffer
	buffer.Grow(len(prefix) + 128)
	buffer.Write(prefix)
	fmt.Fprintf(&buffer, ",\n  \"log-driver\": \"json-file\",\n  \"log-opts\": {\n    \"compress\": %q,\n    \"max-file\": %q,\n    \"max-size\": %q\n  }\n}\n", DefaultLogCompress, DefaultLogMaxFile, maxSize)
	out := buffer.Bytes()
	if !json.Valid(out) {
		return nil, errors.New("merged daemon configuration is not valid JSON")
	}
	return out, nil
}

func selectDefaultLogMaxSize(doc map[string]json.RawMessage, defaultDataRoot string, fsBytes func(string) (uint64, error)) (string, bool) {
	if doc != nil {
		rawDataRoot, hasDataRoot := doc["data-root"]
		rawGraph, hasGraph := doc["graph"]
		if hasDataRoot && hasGraph {
			return DefaultFallbackLogMaxSize, false
		}
		if hasDataRoot || hasGraph {
			rawValue := rawDataRoot
			if !hasDataRoot {
				rawValue = rawGraph
			}
			configured, ok := parseAbsoluteDataRoot(rawValue)
			if !ok {
				return DefaultFallbackLogMaxSize, false
			}
			info, err := os.Stat(configured)
			if err != nil || !info.IsDir() {
				return DefaultFallbackLogMaxSize, false
			}
			return maxSizeForPath(configured, fsBytes)
		}
	}
	probe, ok := nearestExistingDirectory(defaultDataRoot)
	if !ok {
		return DefaultFallbackLogMaxSize, false
	}
	return maxSizeForPath(probe, fsBytes)
}

func parseAbsoluteDataRoot(raw json.RawMessage) (string, bool) {
	trimmed := trimASCIIJSONWhitespace(raw)
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
		return DefaultFallbackLogMaxSize, false
	}
	mib := total / (defaultLogBudgetDivisor * (1 << 20))
	if mib < DefaultMinLogMaxSizeMiB {
		mib = DefaultMinLogMaxSizeMiB
	}
	if mib > DefaultMaxLogMaxSizeMiB {
		mib = DefaultMaxLogMaxSizeMiB
	}
	return fmt.Sprintf("%dm", mib), true
}

func stageTempFile(dir string, content []byte, mode os.FileMode) (string, error) {
	temporary, err := os.CreateTemp(dir, ".daemon.json.vastora-*")
	if err != nil {
		return "", err
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
		return "", err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	keep = true
	return temporaryPath, nil
}

func syncDirectory(dir string) error {
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

func createDaemonConfigExclusive(path string, content []byte, mode os.FileMode, beforeCommit func()) error {
	dir := filepath.Dir(path)
	temporaryPath, err := stageTempFile(dir, content, mode)
	if err != nil {
		return fmt.Errorf("docker runtime: stage %s: %w", path, err)
	}
	defer os.Remove(temporaryPath)
	if beforeCommit != nil {
		beforeCommit()
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("docker runtime: refusing to overwrite %s: file appeared concurrently", path)
		}
		return fmt.Errorf("docker runtime: create %s: %w", path, err)
	}
	if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("docker runtime: clean staged %s: %w", path, err)
	}
	return syncDirectory(dir)
}

func replaceDaemonConfigIfUnchanged(path string, origIdentity fileIdentity, origRaw, content []byte, beforeCommit func()) error {
	dir := filepath.Dir(path)
	temporaryPath, err := stageTempFile(dir, content, origIdentity.mode)
	if err != nil {
		return fmt.Errorf("docker runtime: stage %s: %w", path, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if beforeCommit != nil {
		beforeCommit()
	}
	currentIdentity, currentRaw, exists, err := readRegularNoFollow(path, maxDaemonConfigBytes)
	if err != nil {
		return fmt.Errorf("docker runtime: verify %s before replace: %w", path, err)
	}
	if !exists || currentIdentity != origIdentity || !bytes.Equal(currentRaw, origRaw) {
		return fmt.Errorf("docker runtime: refusing to overwrite %s: file changed concurrently", path)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("docker runtime: replace %s: %w", path, err)
	}
	keep = true
	return syncDirectory(dir)
}
