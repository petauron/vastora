package dockerruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDefaultDaemonLoggingCreatesTwoTiersAndIsIdempotent(t *testing.T) {
	tests := []struct {
		name          string
		fsBytes       uint64
		fsErr         error
		wantMaxSize   string
		wantConfirmed bool
	}{
		{
			name:          "smallDisk10GiB",
			fsBytes:       10 << 30,
			wantMaxSize:   DefaultSmallDiskLogMaxSize,
			wantConfirmed: true,
		},
		{
			name:          "belowThreshold",
			fsBytes:       LargeDiskThresholdBytes - 1,
			wantMaxSize:   DefaultSmallDiskLogMaxSize,
			wantConfirmed: true,
		},
		{
			name:          "atThreshold15GiB",
			fsBytes:       LargeDiskThresholdBytes,
			wantMaxSize:   DefaultLargeDiskLogMaxSize,
			wantConfirmed: true,
		},
		{
			name:          "largeDisk20GiB",
			fsBytes:       20 << 30,
			wantMaxSize:   DefaultLargeDiskLogMaxSize,
			wantConfirmed: true,
		},
		{
			name:          "statErrorFallsBackConservatively",
			fsErr:         errors.New("statfs unavailable"),
			wantMaxSize:   DefaultSmallDiskLogMaxSize,
			wantConfirmed: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "etc", "docker", "daemon.json")
			defaultDataRoot := filepath.Join(root, "var", "lib", "docker")
			if err := os.MkdirAll(filepath.Dir(defaultDataRoot), 0o755); err != nil {
				t.Fatal(err)
			}

			var probed string
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultDataRoot,
				FilesystemBytes: func(path string) (uint64, error) {
					probed = path
					return test.fsBytes, test.fsErr
				},
			})
			if err != nil {
				t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
			}
			if result.Action != DaemonLoggingCreated || result.MaxSize != test.wantMaxSize || result.MaxFile != DefaultLogMaxFile || result.Compress != DefaultLogCompress || result.DataRootConfirmed != test.wantConfirmed {
				t.Fatalf("result = %+v, want created maxSize=%s confirmed=%v", result, test.wantMaxSize, test.wantConfirmed)
			}
			if probed != filepath.Dir(defaultDataRoot) {
				t.Fatalf("probed path = %q, want %q", probed, filepath.Dir(defaultDataRoot))
			}

			info, err := os.Stat(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("mode = %o, want 644", info.Mode().Perm())
			}

			firstContent, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(firstContent, &parsed); err != nil {
				t.Fatalf("written daemon.json is invalid JSON: %v\n%s", err, firstContent)
			}
			if parsed["log-driver"] != "json-file" {
				t.Fatalf("log-driver = %#v, want json-file", parsed["log-driver"])
			}
			opts, ok := parsed["log-opts"].(map[string]any)
			if !ok {
				t.Fatalf("log-opts = %#v", parsed["log-opts"])
			}
			if opts["max-size"] != test.wantMaxSize || opts["max-file"] != DefaultLogMaxFile || opts["compress"] != DefaultLogCompress {
				t.Fatalf("log-opts = %#v", opts)
			}

			// Second invocation must be an idempotent no-op.
			second, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultDataRoot,
				FilesystemBytes: func(string) (uint64, error) {
					t.Fatal("FilesystemBytes should not be called when log settings already exist")
					return 0, nil
				},
			})
			if err != nil {
				t.Fatalf("second EnsureDefaultDaemonLogging failed: %v", err)
			}
			if second.Action != DaemonLoggingPreserved {
				t.Fatalf("second action = %q, want %q", second.Action, DaemonLoggingPreserved)
			}
			secondContent, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstContent, secondContent) {
				t.Fatalf("idempotent rerun modified daemon.json:\nbefore:\n%s\nafter:\n%s", firstContent, secondContent)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingPreservesUnknownFieldsAndPermissions(t *testing.T) {
	root := t.TempDir()
	dockerDir := filepath.Join(root, "etc", "docker")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dockerDir, "daemon.json")
	original := `{
  "storage-driver": "overlay2",
  "live-restore": true,
  "max-concurrent-downloads": 5,
  "registry-mirrors": ["https://mirror.example.test/?a=1&b=2"]
}
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultDataRoot := filepath.Join(root, "var", "lib", "docker")
	if err := os.MkdirAll(defaultDataRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
		ConfigPath:      configPath,
		DefaultDataRoot: defaultDataRoot,
		FilesystemBytes: func(path string) (uint64, error) {
			if path != defaultDataRoot {
				t.Fatalf("probed path = %q, want %q", path, defaultDataRoot)
			}
			return 25 << 30, nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
	}
	if result.Action != DaemonLoggingUpdated || result.MaxSize != DefaultLargeDiskLogMaxSize || !result.DataRootConfirmed {
		t.Fatalf("result = %+v", result)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("existing permissions were changed: got %o, want 600", info.Mode().Perm())
	}

	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), `\u0026`) {
		t.Fatalf("existing URL ampersand was HTML-escaped:\n%s", updated)
	}
	var parsed map[string]any
	if err := json.Unmarshal(updated, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["storage-driver"] != "overlay2" || parsed["live-restore"] != true || parsed["max-concurrent-downloads"] != float64(5) {
		t.Fatalf("unknown fields were not preserved: %#v", parsed)
	}
	mirrors, ok := parsed["registry-mirrors"].([]any)
	if !ok || len(mirrors) != 1 || mirrors[0] != "https://mirror.example.test/?a=1&b=2" {
		t.Fatalf("registry-mirrors = %#v", parsed["registry-mirrors"])
	}
	opts, ok := parsed["log-opts"].(map[string]any)
	if !ok || parsed["log-driver"] != "json-file" || opts["max-size"] != "200m" || opts["max-file"] != "5" || opts["compress"] != "true" {
		t.Fatalf("log configuration = driver:%#v opts:%#v", parsed["log-driver"], parsed["log-opts"])
	}

	entries, err := os.ReadDir(dockerDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "daemon.json" {
		t.Fatalf("unexpected leftover files in %s: %v", dockerDir, entries)
	}
}

func TestEnsureDefaultDaemonLoggingRespectsExplicitLogSettings(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"explicitLocalDriver", `{"log-driver": "local"}` + "\n"},
		{"explicitJSONDriverWithoutOpts", `{"log-driver": "json-file", "storage-driver": "overlay2"}` + "\n"},
		{"explicitLogOptsWithoutDriver", `{"log-opts": {"max-size": "10m", "max-file": "3"}}` + "\n"},
		{"explicitEmptyLogOpts", `{"log-opts": {}}` + "\n"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, []byte(test.content), 0o640); err != nil {
				t.Fatal(err)
			}
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				FilesystemBytes: func(string) (uint64, error) {
					t.Fatal("FilesystemBytes must not run when explicit logging keys are preserved")
					return 0, nil
				},
			})
			if err != nil {
				t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
			}
			if result.Action != DaemonLoggingPreserved {
				t.Fatalf("action = %q, want %q", result.Action, DaemonLoggingPreserved)
			}
			got, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.content {
				t.Fatalf("explicit logging config was modified:\ngot:  %q\nwant: %q", string(got), test.content)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingRejectsInvalidOrNonObjectJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"whitespace", "   \n\t "},
		{"syntaxError", `{"storage-driver": `},
		{"array", `[]`},
		{"null", `null`},
		{"string", `"json-file"`},
		{"number", `42`},
		{"boolean", `true`},
		{"trailingObject", `{}{}`},
		{"trailingGarbage", `{} trailing`},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				FilesystemBytes: func(string) (uint64, error) {
					return 50 << 30, nil
				},
			}); err == nil {
				t.Fatalf("invalid or non-object JSON %q was accepted", test.content)
			}
			got, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.content {
				t.Fatalf("invalid file was overwritten: got %q, want %q", string(got), test.content)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingDataRootSizingPolicy(t *testing.T) {
	root := t.TempDir()
	existingCustomRoot := filepath.Join(root, "mnt", "docker-data")
	if err := os.MkdirAll(existingCustomRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	missingCustomRoot := filepath.Join(root, "mnt", "unmounted-docker-data")
	regularFileRoot := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(regularFileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultRoot := filepath.Join(root, "var", "lib", "docker")
	if err := os.MkdirAll(defaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		input          string
		customBytes    uint64
		wantMaxSize    string
		wantConfirmed  bool
		wantProbedPath string
	}{
		{
			name:           "existingCustomDataRootLarge",
			input:          `{"data-root": "` + existingCustomRoot + `"}`,
			customBytes:    30 << 30,
			wantMaxSize:    "200m",
			wantConfirmed:  true,
			wantProbedPath: existingCustomRoot,
		},
		{
			name:           "existingCustomDataRootSmall",
			input:          `{"data-root": "` + existingCustomRoot + `"}`,
			customBytes:    8 << 30,
			wantMaxSize:    "100m",
			wantConfirmed:  true,
			wantProbedPath: existingCustomRoot,
		},
		{
			name:          "missingCustomDataRootIsConservative",
			input:         `{"data-root": "` + missingCustomRoot + `"}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
		{
			name:          "regularFileDataRootIsConservative",
			input:         `{"data-root": "` + regularFileRoot + `"}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
		{
			name:          "relativeDataRootIsConservative",
			input:         `{"data-root": "var/lib/docker"}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
		{
			name:          "nonStringDataRootIsConservative",
			input:         `{"data-root": 123}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
		{
			name:          "emptyDataRootIsConservative",
			input:         `{"data-root": ""}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
		{
			name:           "existingLegacyGraphLarge",
			input:          `{"graph": "` + existingCustomRoot + `"}`,
			customBytes:    25 << 30,
			wantMaxSize:    "200m",
			wantConfirmed:  true,
			wantProbedPath: existingCustomRoot,
		},
		{
			name:          "conflictingDataRootAndGraphIsConservative",
			input:         `{"data-root": "` + existingCustomRoot + `", "graph": "` + existingCustomRoot + `"}`,
			wantMaxSize:   "100m",
			wantConfirmed: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, []byte(test.input+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var probed string
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultRoot,
				FilesystemBytes: func(path string) (uint64, error) {
					probed = path
					if path == existingCustomRoot {
						return test.customBytes, nil
					}
					return 100 << 30, nil
				},
			})
			if err != nil {
				t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
			}
			if result.Action != DaemonLoggingUpdated || result.MaxSize != test.wantMaxSize || result.DataRootConfirmed != test.wantConfirmed {
				t.Fatalf("result = %+v, want maxSize=%s confirmed=%v", result, test.wantMaxSize, test.wantConfirmed)
			}
			if probed != test.wantProbedPath {
				t.Fatalf("probed = %q, want %q", probed, test.wantProbedPath)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingRejectsSymlinksAndOversizedFiles(t *testing.T) {
	t.Run("symlinkConfig", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.json")
		if err := os.WriteFile(target, []byte(`{"storage-driver":"overlay2"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "daemon.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: link}); err == nil {
			t.Fatal("symlink daemon.json was accepted")
		}
		raw, err := os.ReadFile(target)
		if err != nil || string(raw) != `{"storage-driver":"overlay2"}` {
			t.Fatalf("symlink target was modified: %q err=%v", raw, err)
		}
	})

	t.Run("danglingSymlinkConfig", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "daemon.json")
		if err := os.Symlink(filepath.Join(dir, "missing.json"), link); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: link}); err == nil {
			t.Fatal("dangling symlink daemon.json was accepted")
		}
	})

	t.Run("symlinkParentDirectory", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real-docker")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(dir, "docker-link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: filepath.Join(linkDir, "daemon.json")}); err == nil {
			t.Fatal("symlink parent directory was accepted")
		}
	})

	t.Run("directoryAtConfigPath", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "daemon.json")
		if err := os.MkdirAll(configPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath}); err == nil {
			t.Fatal("directory at daemon.json was accepted")
		}
	})

	t.Run("oversizedConfig", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "daemon.json")
		oversized := `{"pad":"` + strings.Repeat("a", maxDaemonConfigBytes) + `"}`
		if err := os.WriteFile(configPath, []byte(oversized), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath}); err == nil {
			t.Fatal("oversized daemon.json was accepted")
		}
	})
}
