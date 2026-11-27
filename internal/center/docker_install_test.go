package center

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type dockerInstallerHarness struct {
	root        string
	shimBin     string
	scriptPath  string
	osRelease   string
	daemonJSON  string
	dataRoot    string
	eventsPath  string
	aptKeyrings string
	aptSources  string
}

type dockerInstallerHarnessOptions struct {
	osRelease   string
	dfKiB       string
	withPython3 bool
}

func newDockerInstallerHarness(t *testing.T, opts dockerInstallerHarnessOptions) dockerInstallerHarness {
	t.Helper()
	root := t.TempDir()
	shimBin := filepath.Join(root, "bin")
	for _, dir := range []string{
		shimBin,
		filepath.Join(root, "etc", "apt", "sources.list.d"),
		filepath.Join(root, "var", "lib"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	h := dockerInstallerHarness{
		root:        root,
		shimBin:     shimBin,
		scriptPath:  filepath.Join(root, "install-docker.sh"),
		osRelease:   filepath.Join(root, "etc", "os-release"),
		daemonJSON:  filepath.Join(root, "etc", "docker", "daemon.json"),
		dataRoot:    filepath.Join(root, "var", "lib", "docker"),
		eventsPath:  filepath.Join(root, "events.log"),
		aptKeyrings: filepath.Join(root, "etc", "apt", "keyrings"),
		aptSources:  filepath.Join(root, "etc", "apt", "sources.list.d", "docker.sources"),
	}

	if err := os.WriteFile(h.osRelease, []byte(opts.osRelease), 0o644); err != nil {
		t.Fatal(err)
	}

	// Map all host paths checked or written by install-docker.sh into t.TempDir()
	// so CI runner Docker packages and systemd units never leak into tests.
	rewritten := strings.NewReplacer(
		"/etc/os-release", h.osRelease,
		"/etc/docker/daemon.json", h.daemonJSON,
		"/var/lib/docker", h.dataRoot,
		"/etc/apt/keyrings", h.aptKeyrings,
		"/etc/apt/sources.list.d/docker.sources", h.aptSources,
		"/etc/conf.d/docker", filepath.Join(root, "etc", "conf.d", "docker"),
		"/etc/init.d/docker", filepath.Join(root, "etc", "init.d", "docker"),
		"/usr/local/lib/systemd/system", filepath.Join(root, "usr", "local", "lib", "systemd", "system"),
		"/usr/lib/systemd/system", filepath.Join(root, "usr", "lib", "systemd", "system"),
		"/etc/systemd/system", filepath.Join(root, "etc", "systemd", "system"),
		"/run/systemd/system", filepath.Join(root, "run", "systemd", "system"),
		"/lib/systemd/system", filepath.Join(root, "lib", "systemd", "system"),
	).Replace(dockerInstallScript)
	if err := os.WriteFile(h.scriptPath, []byte(rewritten), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"sh", "awk", "cat", "chmod", "dirname", "install", "ln", "mkdir", "mktemp", "mv", "rm", "tee", "tr", "wc"} {
		forwardHostTool(t, shimBin, tool)
	}
	if opts.withPython3 {
		forwardHostTool(t, shimBin, "python3")
	}

	realID, err := exec.LookPath("id")
	if err != nil {
		t.Fatal(err)
	}
	writeShim(t, shimBin, "id", "#!/bin/sh\nif [ \"${1:-}\" = \"-u\" ]; then printf '0\\n'; else exec "+shellQuote(realID)+" \"$@\"; fi\n")
	writeShim(t, shimBin, "df", "#!/bin/sh\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\n'\nprintf '/dev/vda1 "+opts.dfKiB+" 1048576 9437184 10%% /\\n'\n")
	writeShim(t, shimBin, "dpkg", "#!/bin/sh\nif [ \"${1:-}\" = \"--print-architecture\" ]; then printf 'amd64\\n'; exit 0; fi\nexit 0\n")
	writeShim(t, shimBin, "dpkg-query", "#!/bin/sh\nexit 0\n")
	writeShim(t, shimBin, "curl", `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -n "$out" ]; then printf 'fake-gpg-key\n' > "$out"; fi
`)
	writeShim(t, shimBin, "systemctl", "#!/bin/sh\nprintf 'systemctl:%s\\n' \"$*\" >> "+shellQuote(h.eventsPath)+"\n")
	writeShim(t, shimBin, "apt-get", `#!/bin/sh
state=missing
if [ -f `+shellQuote(h.daemonJSON)+` ]; then state=present; fi
printf 'apt-get:%s:daemon_json=%s\n' "$*" "$state" >> `+shellQuote(h.eventsPath)+`
case "$*" in
  *docker-ce*)
    cat > `+shellQuote(filepath.Join(shimBin, "docker"))+` <<'DOCKER_EOF'
#!/bin/sh
printf 'docker:%s\n' "$*" >> `+shellQuote(h.eventsPath)+`
exit 0
DOCKER_EOF
    chmod 0755 `+shellQuote(filepath.Join(shimBin, "docker"))+`
    ;;
esac
`)
	writeShim(t, shimBin, "apk", `#!/bin/sh
state=missing
if [ -f `+shellQuote(h.daemonJSON)+` ]; then state=present; fi
printf 'apk:%s:daemon_json=%s\n' "$*" "$state" >> `+shellQuote(h.eventsPath)+`
cat > `+shellQuote(filepath.Join(shimBin, "docker"))+` <<'DOCKER_EOF'
#!/bin/sh
printf 'docker:%s\n' "$*" >> `+shellQuote(h.eventsPath)+`
exit 0
DOCKER_EOF
chmod 0755 `+shellQuote(filepath.Join(shimBin, "docker"))+`
`)
	writeShim(t, shimBin, "rc-update", "#!/bin/sh\nprintf 'rc-update:%s\\n' \"$*\" >> "+shellQuote(h.eventsPath)+"\n")
	writeShim(t, shimBin, "rc-service", `#!/bin/sh
state=missing
if [ -f `+shellQuote(h.daemonJSON)+` ]; then state=present; fi
printf 'rc-service:%s:daemon_json=%s\n' "$*" "$state" >> `+shellQuote(h.eventsPath)+`
`)
	return h
}

func forwardHostTool(t *testing.T, dir, name string) {
	t.Helper()
	realPath, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("required test host tool %q not found: %v", name, err)
	}
	writeShim(t, dir, name, "#!/bin/sh\nexec "+shellQuote(realPath)+" \"$@\"\n")
}

func writeShim(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (h dockerInstallerHarness) runInDir(dir string, extraEnv ...string) (string, error) {
	realSH, err := exec.LookPath("sh")
	if err != nil {
		return "", err
	}
	command := exec.Command(realSH, h.scriptPath)
	if dir != "" {
		command.Dir = dir
	}
	env := []string{
		"PATH=" + h.shimBin,
		"HOME=" + h.root,
	}
	env = append(env, extraEnv...)
	command.Env = env
	output, err := command.CombinedOutput()
	return string(output), err
}

func (h dockerInstallerHarness) run(extraEnv ...string) (string, error) {
	return h.runInDir("", extraEnv...)
}

func readDaemonJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid JSON in %s: %v\n%s", path, err, raw)
	}
	return parsed
}

func TestDockerInstallScriptConfiguresLoggingBeforePackageInstall(t *testing.T) {
	tests := []struct {
		name        string
		osRelease   string
		dfKiB       string
		wantMaxSize string
		wantEvent   string
	}{
		{"debian13Disk5GiB", "ID=debian\nVERSION_ID=13\n", "5242880", "51m", "apt-get:install --no-remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin:daemon_json=present"},
		{"debian13Disk10GiB", "ID=debian\nVERSION_ID=13\n", "10485760", "102m", "apt-get:install --no-remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin:daemon_json=present"},
		{"ubuntu2404Disk15GiB", "ID=ubuntu\nVERSION_ID=24.04\n", "15728640", "153m", "apt-get:install --no-remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin:daemon_json=present"},
		{"ubuntu2404Disk20GiB", "ID=ubuntu\nVERSION_ID=24.04\n", "20971520", "200m", "apt-get:install --no-remove -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin:daemon_json=present"},
		{"alpine324Disk10GiB", "ID=alpine\nVERSION_ID=3.24.0\n", "10485760", "102m", "apk:add --no-cache docker docker-openrc docker-cli-compose:daemon_json=present"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
				osRelease: test.osRelease,
				dfKiB:     test.dfKiB,
			})
			output, err := h.run()
			if err != nil {
				t.Fatalf("install-docker.sh failed: %v\n%s", err, output)
			}
			events, err := os.ReadFile(h.eventsPath)
			if err != nil || !strings.Contains(string(events), test.wantEvent) {
				t.Fatalf("daemon.json was not staged before package install (err=%v):\n%s", err, events)
			}
			parsed := readDaemonJSONMap(t, h.daemonJSON)
			opts, ok := parsed["log-opts"].(map[string]any)
			if !ok || parsed["log-driver"] != "json-file" || opts["max-size"] != test.wantMaxSize || opts["max-file"] != "5" || opts["compress"] != "true" {
				t.Fatalf("unexpected daemon.json: %#v", parsed)
			}
			info, err := os.Stat(h.daemonJSON)
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("mode = %o err=%v", info.Mode().Perm(), err)
			}
		})
	}
}

func TestDockerInstallScriptSkipsCustomStagedServiceConfig(t *testing.T) {
	tests := []struct {
		name      string
		osRelease string
		relPath   string
	}{
		{"systemdDropIn", "ID=debian\nVERSION_ID=13\n", "etc/systemd/system/docker.service.d/override.conf"},
		{"systemdCustomUnit", "ID=ubuntu\nVERSION_ID=24.04\n", "etc/systemd/system/docker.service"},
		{"systemdRuntimeDropIn", "ID=debian\nVERSION_ID=13\n", "run/systemd/system/docker.service.d/cloud.conf"},
		{"alpineConfD", "ID=alpine\nVERSION_ID=3.24.0\n", "etc/conf.d/docker"},
		{"alpineInitD", "ID=alpine\nVERSION_ID=3.24.0\n", "etc/init.d/docker"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
				osRelease: test.osRelease,
				dfKiB:     "20971520",
			})
			staged := filepath.Join(h.root, test.relPath)
			if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(staged, []byte("# admin override\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			// Verify both standalone and Agent-helper invocations skip before touching daemon.json.
			helperPath := filepath.Join(h.root, "vastora-agent-bin")
			writeShim(t, h.root, "vastora-agent-bin", "#!/bin/sh\nprintf 'helper-should-not-run\\n' >> "+shellQuote(h.eventsPath)+"\nexit 1\n")

			output, err := h.run("VASTORA_AGENT_BIN=" + helperPath)
			if err != nil {
				t.Fatalf("install with custom service config failed: %v\n%s", err, output)
			}
			if !strings.Contains(output, "Existing Docker service configuration detected") || !strings.Contains(output, "skipping default Docker log rotation configuration") {
				t.Fatalf("missing explicit skip notice:\n%s", output)
			}
			if _, err := os.Stat(h.daemonJSON); !os.IsNotExist(err) {
				t.Fatalf("daemon.json was unexpectedly created when %s was staged", test.relPath)
			}
			events, _ := os.ReadFile(h.eventsPath)
			if strings.Contains(string(events), "helper-should-not-run") {
				t.Fatalf("Agent helper ran despite pre-staged service config:\n%s", events)
			}
		})
	}
}

func TestDockerInstallScriptLeavesPreinstalledDockerUntouched(t *testing.T) {
	for _, tc := range []struct {
		name      string
		osRelease string
	}{
		{"debian13", "ID=debian\nVERSION_ID=13\n"},
		{"alpine324", "ID=alpine\nVERSION_ID=3.24.0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
				osRelease: tc.osRelease,
				dfKiB:     "20971520",
			})
			writeShim(t, h.shimBin, "docker", "#!/bin/sh\nexit 0\n")
			if err := os.MkdirAll(filepath.Dir(h.daemonJSON), 0o755); err != nil {
				t.Fatal(err)
			}
			original := "{\"storage-driver\": \"overlay2\"}\n"
			if err := os.WriteFile(h.daemonJSON, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}

			output, err := h.run()
			if err != nil {
				t.Fatalf("existing Docker install failed: %v\n%s", err, output)
			}
			got, err := os.ReadFile(h.daemonJSON)
			if err != nil || string(got) != original {
				t.Fatalf("pre-installed Docker daemon.json changed: got %q err=%v", got, err)
			}
			if _, err := os.Stat(h.eventsPath); !os.IsNotExist(err) {
				events, _ := os.ReadFile(h.eventsPath)
				t.Fatalf("unexpected actions for pre-installed Docker:\n%s", events)
			}
		})
	}
}

func TestDockerInstallScriptStandaloneIsolatedPythonMerge(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available in test environment")
	}

	t.Run("isolatesPythonFromCWDAndPreservesRawValues", func(t *testing.T) {
		h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
			osRelease:   "ID=debian\nVERSION_ID=13\n",
			dfKiB:       "10485760",
			withPython3: true,
		})
		if err := os.MkdirAll(filepath.Dir(h.daemonJSON), 0o755); err != nil {
			t.Fatal(err)
		}
		missingDataRoot := filepath.Join(h.root, "mnt", "unmounted-docker")
		initial := "{\n" +
			"  \"storage-driver\": \"overlay2\",\n" +
			"  \"high-precision\": 123456789012345678901234567890,\n" +
			"  \"huge-exponent\": 1e500,\n" +
			"  \"data-root\": \"" + missingDataRoot + "\"\n" +
			"}\n"
		if err := os.WriteFile(h.daemonJSON, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}

		// Plant a hostile json.py in both CWD and PYTHONPATH to prove python3 -I ignores both.
		untrustedDir := filepath.Join(h.root, "untrusted")
		if err := os.MkdirAll(untrustedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(untrustedDir, "json.py"), []byte("raise SystemExit('untrusted json.py imported')\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		output, err := h.runInDir(untrustedDir, "PYTHONPATH="+untrustedDir)
		if err != nil {
			t.Fatalf("isolated python3 merge failed: %v\n%s", err, output)
		}
		info, err := os.Stat(h.daemonJSON)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o err=%v", info.Mode().Perm(), err)
		}
		raw, err := os.ReadFile(h.daemonJSON)
		if err != nil || !json.Valid(raw) {
			t.Fatalf("merged daemon.json invalid: err=%v\n%s", err, raw)
		}
		for _, want := range []string{
			`"high-precision": 123456789012345678901234567890`,
			`"huge-exponent": 1e500`,
			`"storage-driver": "overlay2"`,
			`"log-driver": "json-file"`,
			`"max-size": "50m"`,
		} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("missing %q in:\n%s", want, raw)
			}
		}
	})

	t.Run("preservesExplicitLoggingAndRejectsInvalidJSON", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
			wantErr bool
		}{
			{"explicitDriver", "{\"log-driver\": \"local\"}\n", false},
			{"explicitOpts", "{\"log-opts\": {\"max-size\": \"10m\"}}\n", false},
			{"invalidJSON", `{broken`, true},
			{"nonObjectArray", `[]`, true},
			{"nonObjectNull", `null`, true},
			{"nonStandardNaN", `{"n": NaN}`, true},
			{"leadingNBSP", "\u00a0{}", true},
			{"emptyFile", ``, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
					osRelease:   "ID=debian\nVERSION_ID=13\n",
					dfKiB:       "20971520",
					withPython3: true,
				})
				if err := os.MkdirAll(filepath.Dir(h.daemonJSON), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(h.daemonJSON, []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
				output, err := h.run()
				if tc.wantErr && err == nil {
					t.Fatalf("expected error for %s, got success:\n%s", tc.name, output)
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("unexpected error for %s: %v\n%s", tc.name, err, output)
				}
				got, err := os.ReadFile(h.daemonJSON)
				if err != nil || string(got) != tc.content {
					t.Fatalf("file was modified: got %q want %q err=%v", got, tc.content, err)
				}
				if tc.wantErr {
					if _, statErr := os.Stat(h.eventsPath); !os.IsNotExist(statErr) {
						t.Fatal("apt-get ran despite invalid daemon.json")
					}
				}
			})
		}
	})
}

func TestDockerInstallScriptPreservesExistingFileWithoutPython3(t *testing.T) {
	h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
		osRelease: "ID=alpine\nVERSION_ID=3.24.0\n",
		dfKiB:     "20971520",
	})
	if err := os.MkdirAll(filepath.Dir(h.daemonJSON), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\"storage-driver\": \"overlay2\"}\n"
	if err := os.WriteFile(h.daemonJSON, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := h.run()
	if err != nil {
		t.Fatalf("standalone no-parser install failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "preserved unchanged") || !strings.Contains(output, "default Docker log rotation was not configured") {
		t.Fatalf("missing clear no-parser notice:\n%s", output)
	}
	got, err := os.ReadFile(h.daemonJSON)
	if err != nil || string(got) != original {
		t.Fatalf("existing daemon.json was altered without python3: got %q err=%v", got, err)
	}
}

func TestDockerInstallScriptUsesVerifiedAgentHelperAndAgentInstallOrder(t *testing.T) {
	h := newDockerInstallerHarness(t, dockerInstallerHarnessOptions{
		osRelease: "ID=debian\nVERSION_ID=13\n",
		dfKiB:     "10485760",
	})

	helperPath := filepath.Join(h.root, "vastora-agent-bin")
	helperScript := "#!/bin/sh\n" +
		"printf 'helper:%s\\n' \"$*\" >> " + shellQuote(h.eventsPath) + "\n" +
		"mkdir -p " + shellQuote(filepath.Dir(h.daemonJSON)) + "\n" +
		"printf '{\"log-driver\":\"json-file\",\"log-opts\":{\"compress\":\"true\",\"max-file\":\"5\",\"max-size\":\"102m\"}}\\n' > " + shellQuote(h.daemonJSON) + "\n"
	if err := os.WriteFile(helperPath, []byte(helperScript), 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := h.run("VASTORA_AGENT_BIN=" + helperPath)
	if err != nil {
		t.Fatalf("agent-assisted Docker install failed: %v\n%s", err, output)
	}
	events, err := os.ReadFile(h.eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	eventText := string(events)
	helperIndex := strings.Index(eventText, "helper:agent prepare-docker-logging")
	aptIndex := strings.Index(eventText, "apt-get:install --no-remove -y docker-ce")
	if helperIndex < 0 || aptIndex < 0 || helperIndex > aptIndex {
		t.Fatalf("verified Agent helper did not run before apt-get install docker-ce:\n%s", eventText)
	}

	script := renderAgentInstallScript(AgentEnrollmentInstallProfile{
		CenterURL:    "https://center.example.com",
		Capabilities: NodeCapabilities{Docker: true},
	}, "https://center.example.com")
	versionCheck := strings.Index(script, "The downloaded Agent failed its version check.")
	switchPrompt := strings.Index(script, "Switch this Agent to the requested Center?")
	dockerBootstrap := strings.Index(script, `VASTORA_AGENT_BIN="$temporary" sh "$docker_installer"`)
	switchBegin := strings.Index(script, "switch-control-plane begin")
	if versionCheck < 0 || switchPrompt < 0 || dockerBootstrap < 0 || switchBegin < 0 || !(versionCheck < switchPrompt && switchPrompt < dockerBootstrap && dockerBootstrap < switchBegin) {
		t.Fatalf("unexpected Docker bootstrap order: version=%d prompt=%d docker=%d switchBegin=%d", versionCheck, switchPrompt, dockerBootstrap, switchBegin)
	}
}
