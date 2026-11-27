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

func TestEnsureDefaultDaemonLoggingSizingAndIdempotency(t *testing.T) {
	tests := []struct {
		name          string
		fsBytes       uint64
		fsErr         error
		wantMaxSize   string
		wantConfirmed bool
	}{
		{"tinyDisk50MiB", 50 << 20, nil, "1m", true},
		{"smallDisk5GiB", 5 << 30, nil, "51m", true},
		{"disk10GiB", 10 << 30, nil, "102m", true},
		{"disk15GiB", 15 << 30, nil, "153m", true},
		{"disk20GiB", 20 << 30, nil, "200m", true},
		{"largeDisk200GiB", 200 << 30, nil, "200m", true},
		{"statErrorFallback", 0, errors.New("statfs unavailable"), DefaultFallbackLogMaxSize, false},
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
			fs := func(path string) (uint64, error) {
				probed = path
				return test.fsBytes, test.fsErr
			}
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultDataRoot,
				FilesystemBytes: fs,
			})
			if err != nil {
				t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
			}
			if result.Action != DaemonLoggingCreated || result.MaxSize != test.wantMaxSize || result.MaxFile != DefaultLogMaxFile || result.Compress != DefaultLogCompress || result.DataRootConfirmed != test.wantConfirmed {
				t.Fatalf("result = %+v, want created maxSize=%s confirmed=%v", result, test.wantMaxSize, test.wantConfirmed)
			}
			if probed != filepath.Dir(defaultDataRoot) {
				t.Fatalf("probed = %q, want %q", probed, filepath.Dir(defaultDataRoot))
			}

			info, err := os.Stat(configPath)
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("stat = %v mode=%o", err, info.Mode().Perm())
			}
			firstContent, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(firstContent, &parsed); err != nil {
				t.Fatalf("invalid JSON: %v\n%s", err, firstContent)
			}
			opts, ok := parsed["log-opts"].(map[string]any)
			if !ok || parsed["log-driver"] != "json-file" || opts["max-size"] != test.wantMaxSize || opts["max-file"] != DefaultLogMaxFile || opts["compress"] != DefaultLogCompress {
				t.Fatalf("parsed = %#v", parsed)
			}

			failFS := func(string) (uint64, error) {
				t.Fatal("FilesystemBytes must not run on idempotent rerun")
				return 0, nil
			}
			second, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultDataRoot,
				FilesystemBytes: failFS,
			})
			if err != nil || second.Action != DaemonLoggingPreserved {
				t.Fatalf("rerun = %+v err=%v", second, err)
			}
			secondContent, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(firstContent, secondContent) {
				t.Fatalf("idempotent rerun changed file: err=%v", err)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingPreservesRawValuesAndPermissions(t *testing.T) {
	root := t.TempDir()
	dockerDir := filepath.Join(root, "etc", "docker")
	defaultDataRoot := filepath.Join(root, "var", "lib", "docker")
	if err := os.MkdirAll(dockerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(defaultDataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dockerDir, "daemon.json")
	original := "{\n" +
		"  \"storage-driver\": \"overlay2\",\n" +
		"  \"high-precision\": 123456789012345678901234567890,\n" +
		"  \"huge-exponent\": 1e500,\n" +
		"  \"registry-mirrors\": [\"https://mirror.example.test/?a=1&b=2\"]\n" +
		"}\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	fs := func(path string) (uint64, error) {
		if path != defaultDataRoot {
			t.Fatalf("probed = %q, want %q", path, defaultDataRoot)
		}
		return 25 << 30, nil
	}
	result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
		ConfigPath:      configPath,
		DefaultDataRoot: defaultDataRoot,
		FilesystemBytes: fs,
	})
	if err != nil || result.Action != DaemonLoggingUpdated || result.MaxSize != "200m" || !result.DataRootConfirmed {
		t.Fatalf("result = %+v err=%v", result, err)
	}

	info, err := os.Stat(configPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: err=%v mode=%o", err, info.Mode().Perm())
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(updated) {
		t.Fatalf("merged output is not valid JSON:\n%s", updated)
	}
	for _, preserved := range []string{
		`"storage-driver": "overlay2"`,
		`"high-precision": 123456789012345678901234567890`,
		`"huge-exponent": 1e500`,
		`"registry-mirrors": ["https://mirror.example.test/?a=1&b=2"]`,
		`"log-driver": "json-file"`,
		`"max-size": "200m"`,
	} {
		if !strings.Contains(string(updated), preserved) {
			t.Fatalf("missing %q in:\n%s", preserved, updated)
		}
	}
	entries, err := os.ReadDir(dockerDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "daemon.json" {
		t.Fatalf("unexpected files in %s: %v err=%v", dockerDir, entries, err)
	}
}

func TestEnsureDefaultDaemonLoggingRespectsExplicitLogSettingsAndEmptyObject(t *testing.T) {
	failFS := func(string) (uint64, error) {
		t.Fatal("FilesystemBytes must not run when explicit logging keys exist")
		return 0, nil
	}
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"explicitLocalDriver", "{\"log-driver\": \"local\"}\n"},
		{"explicitJSONDriverOnly", "{\"log-driver\": \"json-file\"}\n"},
		{"explicitLogOptsOnly", "{\"log-opts\": {\"max-size\": \"10m\"}}\n"},
		{"explicitEmptyLogOpts", "{\"log-opts\": {}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, []byte(tc.content), 0o640); err != nil {
				t.Fatal(err)
			}
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, FilesystemBytes: failFS})
			if err != nil || result.Action != DaemonLoggingPreserved {
				t.Fatalf("result = %+v err=%v", result, err)
			}
			got, err := os.ReadFile(configPath)
			if err != nil || string(got) != tc.content {
				t.Fatalf("explicit config changed: got %q err=%v", got, err)
			}
		})
	}

	t.Run("emptyObjectGetsDefaults", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "daemon.json")
		if err := os.WriteFile(configPath, []byte(" { \n } "), 0o644); err != nil {
			t.Fatal(err)
		}
		fs := func(string) (uint64, error) { return 10 << 30, nil }
		result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, DefaultDataRoot: t.TempDir(), FilesystemBytes: fs})
		if err != nil || result.Action != DaemonLoggingUpdated || result.MaxSize != "102m" {
			t.Fatalf("result = %+v err=%v", result, err)
		}
		raw, err := os.ReadFile(configPath)
		if err != nil || !json.Valid(raw) {
			t.Fatalf("invalid output: err=%v raw=%s", err, raw)
		}
	})
}

func TestEnsureDefaultDaemonLoggingRejectsInvalidJSONAndUnicodeWhitespace(t *testing.T) {
	largeFS := func(string) (uint64, error) { return 50 << 30, nil }
	cases := []struct {
		name    string
		content []byte
	}{
		{"empty", []byte("")},
		{"asciiWhitespace", []byte("   \n\t ")},
		{"leadingNBSP", []byte("\u00a0{}")},
		{"trailingNBSP", []byte("{}\u00a0")},
		{"utf8BOM", []byte("\ufeff{}")},
		{"verticalTab", []byte("\v{}")},
		{"invalidUTF8", []byte{0xff, '{', '}'}},
		{"syntaxError", []byte(`{"storage-driver": `)},
		{"array", []byte(`[]`)},
		{"null", []byte(`null`)},
		{"string", []byte(`"json-file"`)},
		{"number", []byte(`42`)},
		{"boolean", []byte(`true`)},
		{"trailingObject", []byte(`{}{}`)},
		{"trailingGarbage", []byte(`{} extra`)},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, test.content, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, FilesystemBytes: largeFS}); err == nil {
				t.Fatalf("invalid JSON %q was accepted", test.content)
			}
			got, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(got, test.content) {
				t.Fatalf("invalid file was modified: got %q want %q err=%v", got, test.content, err)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingDataRootSizingPolicy(t *testing.T) {
	root := t.TempDir()
	existingCustomRoot := filepath.Join(root, "mnt", "docker-data")
	defaultRoot := filepath.Join(root, "var", "lib", "docker")
	if err := os.MkdirAll(existingCustomRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(defaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	missingCustomRoot := filepath.Join(root, "mnt", "unmounted-docker-data")
	regularFileRoot := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(regularFileRoot, []byte("x"), 0o600); err != nil {
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
		{"existingCustomLarge", `{"data-root": "` + existingCustomRoot + `"}`, 30 << 30, "200m", true, existingCustomRoot},
		{"existingCustom15GiB", `{"data-root": "` + existingCustomRoot + `"}`, 15 << 30, "153m", true, existingCustomRoot},
		{"existingCustomSmall", `{"data-root": "` + existingCustomRoot + `"}`, 8 << 30, "81m", true, existingCustomRoot},
		{"missingCustomRoot", `{"data-root": "` + missingCustomRoot + `"}`, 0, DefaultFallbackLogMaxSize, false, ""},
		{"fileAsDataRoot", `{"data-root": "` + regularFileRoot + `"}`, 0, DefaultFallbackLogMaxSize, false, ""},
		{"relativeDataRoot", `{"data-root": "var/lib/docker"}`, 0, DefaultFallbackLogMaxSize, false, ""},
		{"nonStringDataRoot", `{"data-root": 123}`, 0, DefaultFallbackLogMaxSize, false, ""},
		{"emptyDataRoot", `{"data-root": ""}`, 0, DefaultFallbackLogMaxSize, false, ""},
		{"existingLegacyGraph", `{"graph": "` + existingCustomRoot + `"}`, 25 << 30, "200m", true, existingCustomRoot},
		{"conflictingDataRootAndGraph", `{"data-root": "` + existingCustomRoot + `", "graph": "` + existingCustomRoot + `"}`, 0, DefaultFallbackLogMaxSize, false, ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "daemon.json")
			if err := os.WriteFile(configPath, []byte(test.input+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var probed string
			fs := func(path string) (uint64, error) {
				probed = path
				if path == existingCustomRoot {
					return test.customBytes, nil
				}
				return 100 << 30, nil
			}
			result, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{
				ConfigPath:      configPath,
				DefaultDataRoot: defaultRoot,
				FilesystemBytes: fs,
			})
			if err != nil {
				t.Fatalf("EnsureDefaultDaemonLogging failed: %v", err)
			}
			if result.Action != DaemonLoggingUpdated || result.MaxSize != test.wantMaxSize || result.DataRootConfirmed != test.wantConfirmed || probed != test.wantProbedPath {
				t.Fatalf("result=%+v probed=%q, want maxSize=%s confirmed=%v probed=%q", result, probed, test.wantMaxSize, test.wantConfirmed, test.wantProbedPath)
			}
		})
	}
}

func TestEnsureDefaultDaemonLoggingPreservesConcurrentWrites(t *testing.T) {
	fs := func(string) (uint64, error) { return 20 << 30, nil }

	t.Run("noClobberOnConcurrentCreate", func(t *testing.T) {
		root := t.TempDir()
		configPath := filepath.Join(root, "etc", "docker", "daemon.json")
		concurrent := "{\"storage-driver\": \"vfs\"}\n"
		hook := func() {
			if err := os.WriteFile(configPath, []byte(concurrent), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ensureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, DefaultDataRoot: root, FilesystemBytes: fs}, hook); err == nil {
			t.Fatal("concurrently created daemon.json was overwritten")
		}
		got, err := os.ReadFile(configPath)
		if err != nil || string(got) != concurrent {
			t.Fatalf("concurrent file was clobbered: got %q err=%v", got, err)
		}
	})

	t.Run("noClobberOnConcurrentDirectory", func(t *testing.T) {
		root := t.TempDir()
		configPath := filepath.Join(root, "etc", "docker", "daemon.json")
		hook := func() {
			if err := os.MkdirAll(configPath, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ensureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, DefaultDataRoot: root, FilesystemBytes: fs}, hook); err == nil {
			t.Fatal("concurrent directory at daemon.json was accepted")
		}
		entries, err := os.ReadDir(configPath)
		if err != nil || len(entries) != 0 {
			t.Fatalf("unexpected entries in concurrent directory: %v err=%v", entries, err)
		}
	})

	t.Run("abortOnConcurrentEdit", func(t *testing.T) {
		root := t.TempDir()
		configPath := filepath.Join(root, "daemon.json")
		if err := os.WriteFile(configPath, []byte("{\"storage-driver\": \"overlay2\"}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		operatorEdit := "{\"storage-driver\": \"overlay2\", \"log-driver\": \"local\"}\n"
		hook := func() {
			if err := os.WriteFile(configPath, []byte(operatorEdit), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ensureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, DefaultDataRoot: root, FilesystemBytes: fs}, hook); err == nil {
			t.Fatal("concurrently edited daemon.json was overwritten")
		}
		got, err := os.ReadFile(configPath)
		if err != nil || string(got) != operatorEdit {
			t.Fatalf("operator edit was clobbered: got %q err=%v", got, err)
		}
	})

	t.Run("abortOnConcurrentReplaceSameContent", func(t *testing.T) {
		root := t.TempDir()
		configPath := filepath.Join(root, "daemon.json")
		initial := "{\"storage-driver\": \"overlay2\"}\n"
		if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
		replacement := filepath.Join(root, "replacement.json")
		hook := func() {
			if err := os.WriteFile(replacement, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, configPath); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ensureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: configPath, DefaultDataRoot: root, FilesystemBytes: fs}, hook); err == nil {
			t.Fatal("concurrently replaced daemon.json was overwritten")
		}
		info, err := os.Stat(configPath)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("replacement permissions lost: err=%v mode=%o", err, info.Mode().Perm())
		}
	})
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
