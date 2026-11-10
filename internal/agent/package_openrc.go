package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/hostservice"
)

// OpenRC invokes only this executor-owned launcher specification. Environment
// files are parsed as data; even application-owned values never become shell.
type openRCProcess struct {
	Version          int                     `json:"version"`
	Executable       string                  `json:"executable"`
	SHA256           string                  `json:"sha256"`
	Arguments        []string                `json:"arguments"`
	User             string                  `json:"user"`
	RuntimeDirectory string                  `json:"runtimeDirectory"`
	Environment      string                  `json:"environment"`
	Credentials      map[string]string       `json:"credentials"`
	Limits           *catalog.ResourceLimits `json:"limits,omitempty"`
}

func compileOpenRCProcess(applicationID string, unit nativeUnitPlan, executable, executableSHA, environmentPath string, credentials map[string]string, config map[string]any, secrets map[string]string, values map[string]string, icmp bool) ([]byte, []byte, error) {
	arguments, err := runtimeStrings(unit.Process.Arguments, config, secrets, values)
	if err != nil {
		return nil, nil, err
	}
	// The installer owns this stable launcher. Never bind a persistent unit
	// to a temporary stage-native-package or acceptance executable.
	agentBinary := "/usr/local/bin/vastora"
	launcher, err := os.Lstat(agentBinary)
	if err != nil || !launcher.Mode().IsRegular() || launcher.Mode().Perm()&0022 != 0 {
		return nil, nil, errors.New("agent: installed canonical Agent launcher unavailable")
	}
	owner, ok := launcher.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 {
		return nil, nil, errors.New("agent: installed Agent launcher is not root owned")
	}

	spec := openRCProcess{Version: 1, Executable: executable, SHA256: executableSHA, Arguments: arguments, User: unit.Process.User, RuntimeDirectory: filepath.Join("/run", unit.RuntimeDirectory), Environment: environmentPath, Credentials: credentials, Limits: unit.Process.Limits}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(raw)
	specPath := filepath.Join(unit.ConfigDirectory, "process.json")
	args := strings.Join([]string{"agent", "run-native", "--spec", hostservice.Quote(specPath), "--sha256", hex.EncodeToString(digest[:])}, " ")
	var script strings.Builder
	fmt.Fprintf(&script, "#!/sbin/openrc-run\n# Managed by Vastora\n# Application: %s\n", applicationID)
	fmt.Fprintf(&script, "description='Managed native package'\nsupervisor=supervise-daemon\ncommand=%s\ncommand_args=%s\ncommand_user=%s\n", hostservice.Quote(agentBinary), hostservice.Quote(args), hostservice.Quote(spec.User))
	script.WriteString("respawn_delay=5\nrespawn_max=0\nretry='TERM/30/KILL/5'\numask=0077\nno_new_privs=yes\n")
	if spec.User != "root" {
		caps, err := openRCCapabilities(unit.Process.BindService, icmp)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&script, "capabilities=%s\n", hostservice.Quote(caps))
	}
	if limits := unit.Process.Limits; limits != nil {
		var settings []string
		if limits.MemoryBytes > 0 {
			settings = append(settings, fmt.Sprintf("memory.max %d", limits.MemoryBytes))
		}
		if limits.Pids > 0 {
			settings = append(settings, fmt.Sprintf("pids.max %d", limits.Pids))
		}
		if limits.CPUMillis > 0 {
			settings = append(settings, fmt.Sprintf("cpu.max %d 100000", limits.CPUMillis*100))
		}
		fmt.Fprintf(&script, "rc_cgroup_settings=%s\n", hostservice.Quote(strings.Join(settings, "\n")))
	}
	name, _ := hostservice.Name(unit.Name)
	fmt.Fprintf(&script, "output_logger='/usr/bin/logger -t %s'\nerror_logger='/usr/bin/logger -t %s'\n", name, name)
	script.WriteString("extra_commands='vastora_pid'\ndepend() { need net; use logger tailscale; }\n")
	fmt.Fprintf(&script, "start_pre() { %s agent prepare-native --spec %s --sha256 %s; }\n", hostservice.Quote(agentBinary), hostservice.Quote(specPath), hostservice.Quote(hex.EncodeToString(digest[:])))
	fmt.Fprintf(&script, "start_post() { %s agent check-native --spec %s --sha256 %s || { supervise_stop; return 1; }; }\n", hostservice.Quote(agentBinary), hostservice.Quote(specPath), hostservice.Quote(hex.EncodeToString(digest[:])))
	script.WriteString("vastora_pid() {\n if service_started || service_starting; then\n  service_get_value child_pid\n else\n  printf '0\\n'\n fi\n}\n")
	return []byte(script.String()), raw, nil
}

func openRCCapabilities(bind, icmp bool) (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return "", errors.New("agent: kernel capability ceiling unavailable")
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || last < 13 || last > 63 {
		return "", errors.New("agent: unsupported kernel capabilities")
	}
	var caps []string
	for n := 0; n <= last; n++ {
		prefix := "!"
		if n == 10 && bind || n == 13 && icmp {
			prefix = "^"
		}
		caps = append(caps, prefix+strconv.Itoa(n))
	}
	return strings.Join(caps, ","), nil
}

func readOpenRCProcess(path, digest string) (openRCProcess, error) {
	var spec openRCProcess
	if !strings.HasPrefix(path, "/etc/vastora/packages/") || filepath.Clean(path) != path || filepath.Base(path) != "process.json" {
		return spec, errors.New("agent: native launcher path outside managed installation")
	}
	if err := checkPackageParents(path); err != nil {
		return spec, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 1<<20 {
		return spec, errors.New("agent: unsafe native launcher specification")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return spec, errors.New("agent: native launcher must be root owned")
	}
	raw, err := os.ReadFile(path)
	hash := sha256.Sum256(raw)
	if err != nil || len(digest) != 64 || hex.EncodeToString(hash[:]) != digest {
		return spec, errors.New("agent: native launcher ownership changed")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&spec) != nil || dec.Decode(&extra) != io.EOF || spec.Version != 1 {
		return spec, errors.New("agent: invalid native launcher specification")
	}
	base := filepath.Dir(path)
	if spec.Environment != filepath.Join(base, "environment") || !strings.HasPrefix(spec.Executable, "/opt/vastora/packages/") || filepath.Clean(spec.Executable) != spec.Executable || !strings.HasPrefix(spec.RuntimeDirectory, "/run/vastora-pkg-") || filepath.Clean(spec.RuntimeDirectory) != spec.RuntimeDirectory || strings.Count(strings.TrimPrefix(spec.RuntimeDirectory, "/run/"), "/") != 0 {
		return spec, errors.New("agent: native launcher resource escapes managed paths")
	}
	if spec.User != "root" && !strings.HasPrefix(spec.User, "vp-") {
		return spec, errors.New("agent: native launcher has no dedicated account")
	}
	for name, source := range spec.Credentials {
		if name == "" || name == "environment.json" || filepath.Base(name) != name || strings.ContainsAny(name, "\r\n\\") || !(strings.HasPrefix(source, base+"/") || strings.HasPrefix(source, "/var/lib/"+filepath.Base(filepath.Dir(base))+"-")) || filepath.Clean(source) != source {
			return spec, errors.New("agent: invalid native launcher credential")
		}
	}
	return spec, nil
}

// PrepareOpenRCProcess is the root start_pre hook. /run is reconstructed on
// every boot/start, while durable credentials and identity remain untouched.
func PrepareOpenRCProcess(path, digest string) error {
	if os.Geteuid() != 0 {
		return errors.New("agent: native runtime preparation requires root")
	}
	spec, err := readOpenRCProcess(path, digest)
	if err != nil {
		return err
	}
	account, err := user.Lookup(spec.User)
	if err != nil {
		return errors.New("agent: native runtime account unavailable")
	}
	uid, e1 := strconv.Atoi(account.Uid)
	gid, e2 := strconv.Atoi(account.Gid)
	if e1 != nil || e2 != nil || uid < 0 || gid < 0 {
		return errors.New("agent: invalid native account IDs")
	}
	if spec.User != "root" {
		// Arguments may contain secrets: only this dedicated service group may
		// read the root-owned spec. Apply that restriction before opening parent
		// search permissions; environment and durable credentials remain 0600.
		if err := os.Chown(path, 0, gid); err != nil {
			return err
		}
		if err := os.Chmod(path, 0640); err != nil {
			return err
		}
		for _, file := range []string{path, spec.Executable} {
			base := "/etc/vastora"
			if file == spec.Executable {
				base = "/opt/vastora"
			}
			if err := prepareOpenRCSearchPath(file, base); err != nil {
				return err
			}
		}
	}
	if err := checkPackageParents(filepath.Join(spec.RuntimeDirectory, "placeholder")); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.RuntimeDirectory, 0700); err != nil {
		return err
	}
	if err := os.Chown(spec.RuntimeDirectory, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(spec.RuntimeDirectory, 0700); err != nil {
		return err
	}
	environment, err := readNativeEnvironment(spec.Environment)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(environment)
	if err != nil {
		return err
	}
	files := map[string][]byte{"environment.json": raw}
	for name, source := range spec.Credentials {
		if err := checkPackageParents(source); err != nil {
			return err
		}
		s, err := os.Lstat(source)
		if err != nil || !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 || s.Size() > 1<<20 {
			return errors.New("agent: unsafe native credential source")
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return errors.New("agent: native credential source unavailable")
		}
		files[name] = content
	}
	for name, content := range files {
		target := filepath.Join(spec.RuntimeDirectory, name)
		if err := writeOpenRCRuntimeFile(target, content, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func prepareOpenRCSearchPath(file, base string) error {
	if !strings.HasPrefix(file, base+"/") || filepath.Clean(file) != file {
		return errors.New("agent: native search path outside managed installation")
	}
	for directory := filepath.Dir(file); strings.HasPrefix(directory, base); directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("agent: protected root-owned native search directory required")
		}
		if info.Mode().Perm()&0011 != 0011 {
			if err := os.Chmod(directory, info.Mode().Perm()|0011); err != nil {
				return err
			}
		}
	}
	return nil
}
func readNativeEnvironment(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("agent: native environment unavailable")
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, errors.New("agent: invalid native environment")
		}
		for i, c := range key {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
				return nil, errors.New("agent: invalid native environment key")
			}
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("agent: duplicate native environment")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil || strings.ContainsRune(decoded, 0) {
			return nil, errors.New("agent: invalid native environment value")
		}
		result[key] = decoded
	}
	return result, nil
}
func RunOpenRCProcess(path, digest string) error {
	spec, err := readOpenRCProcess(path, digest)
	if err != nil {
		return err
	}
	account, err := user.Lookup(spec.User)
	if err != nil || account.Uid != strconv.Itoa(os.Geteuid()) || account.Gid != strconv.Itoa(os.Getegid()) {
		return errors.New("agent: native launcher identity differs")
	}
	if err := checkPackageParents(spec.Executable); err != nil {
		return err
	}
	info, err := os.Lstat(spec.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 256<<20 {
		return errors.New("agent: unsafe native executable")
	}
	file, err := os.Open(spec.Executable)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	file.Close()
	if copyErr != nil || hex.EncodeToString(hash.Sum(nil)) != spec.SHA256 {
		return errors.New("agent: native executable ownership changed")
	}
	raw, err := os.ReadFile(filepath.Join(spec.RuntimeDirectory, "environment.json"))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("agent: prepared native environment unavailable")
	}
	environment := map[string]string{}
	if json.Unmarshal(raw, &environment) != nil {
		return errors.New("agent: prepared native environment invalid")
	}
	environment["PATH"] = "/usr/sbin:/usr/bin:/sbin:/bin"
	var env []string
	for key, value := range environment {
		if strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("agent: invalid native exec environment")
		}
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return syscall.Exec(spec.Executable, append([]string{spec.Executable}, spec.Arguments...), env)
}

func (manager SystemdHostApplicationManager) service(ctx context.Context, action, unit string) error {
	kind := manager.InitSystem
	if kind == "" {
		kind = hostservice.Systemd
	}
	return hostservice.Action(ctx, kind, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, manager.run(ctx, name, args...)
	}, action, unit)
}
func (manager SystemdHostApplicationManager) accountShell() string {
	if manager.InitSystem == hostservice.OpenRC {
		return "/sbin/nologin"
	}
	return "/usr/sbin/nologin"
}

func (manager SystemdHostApplicationManager) createNativeAccount(ctx context.Context, name, applicationID string) error {
	if manager.InitSystem != hostservice.OpenRC {
		if err := manager.run(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "Vastora "+applicationID, name); err != nil {
			return errors.New("agent: native managed user creation failed")
		}
		return nil
	}
	if err := manager.run(ctx, "addgroup", "-S", name); err != nil {
		return errors.New("agent: native managed group creation failed")
	}
	if err := manager.run(ctx, "adduser", "-S", "-D", "-H", "-h", "/nonexistent", "-s", "/sbin/nologin", "-G", name, "-g", "Vastora "+applicationID, name); err != nil {
		return errors.New("agent: native managed user creation failed; inspect partially created group")
	}
	return nil
}

// Chown the exclusively opened temporary file before rename. Never follow a
// service user's replacement symlink in a runtime directory as root.
func writeOpenRCRuntimeFile(path string, raw []byte, uid, gid int) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".credential-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (manager SystemdHostApplicationManager) removeNativeAccount(ctx context.Context, kind, name string) error {
	command := "userdel"
	if kind == "group" {
		command = "groupdel"
	}
	if manager.InitSystem == hostservice.OpenRC {
		command = "deluser"
		if kind == "group" {
			command = "delgroup"
		}
	}
	return manager.run(ctx, command, name)
}

// OpenRC must wait for exec of the pinned application, not just fork of this
// credential launcher. Otherwise the controller can mistake the launcher PID
// for Xray and reject every cold start.
func WaitOpenRCProcess(ctx context.Context, path, digest string) error {
	spec, err := readOpenRCProcess(path, digest)
	if err != nil {
		return err
	}
	installed, err := os.Stat(spec.Executable)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unit := filepath.Base(spec.RuntimeDirectory) + ".service"
	for {
		invocation, err := hostservice.OpenRCInvocation(ctx, nil, unit)
		if err == nil && invocation.PID > 1 {
			running, err := os.Stat(filepath.Join("/proc", strconv.Itoa(invocation.PID), "exe"))
			if err == nil && os.SameFile(installed, running) {
				return verifyOpenRCLimits("", invocation.PID, spec.Limits)
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: native application did not complete exec")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Reject a GNU-linked candidate before stopping a working Alpine service. A
// missing glibc loader must not become a late, partially applied upgrade.
func verifyOpenRCExecutable(path string) error {
	binary, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer binary.Close()
	for _, program := range binary.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
		if err != nil || len(raw) > 4096 {
			return errors.New("agent: invalid native ELF interpreter")
		}
		interpreter := strings.TrimSuffix(string(raw), "\x00")
		if interpreter != "/lib/ld-musl-x86_64.so.1" && interpreter != "/lib/ld-musl-aarch64.so.1" {
			return errors.New("agent: OpenRC package requires a static or native musl executable; glibc compatibility is not supported")
		}
	}
	return nil
}

// OpenRC can skip unavailable cgroup controllers. A declared bound must be
// observed on the actual child, rather than inferred from rc_cgroup_settings.
func verifyOpenRCLimits(root string, pid int, limits *catalog.ResourceLimits) error {
	if limits == nil || limits.MemoryBytes == 0 && limits.Pids == 0 && limits.CPUMillis == 0 {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(root, "/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return errors.New("agent: native resource limit evidence unavailable")
	}
	var group string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "0::/") {
			if group != "" {
				return errors.New("agent: ambiguous native cgroup")
			}
			group = strings.TrimPrefix(line, "0::")
		}
	}
	if group == "" || filepath.Clean(group) != group || strings.Contains(group, "..") {
		return errors.New("agent: native unified cgroup unavailable")
	}
	base := filepath.Join(root, "/sys/fs/cgroup", group)
	expected := map[string]string{}
	if limits.MemoryBytes > 0 {
		expected["memory.max"] = strconv.FormatInt(limits.MemoryBytes, 10)
	}
	if limits.Pids > 0 {
		expected["pids.max"] = strconv.FormatInt(limits.Pids, 10)
	}
	if limits.CPUMillis > 0 {
		expected["cpu.max"] = fmt.Sprintf("%d 100000", limits.CPUMillis*100)
	}
	for name, want := range expected {
		raw, err := os.ReadFile(filepath.Join(base, name))
		if err != nil || strings.TrimSpace(string(raw)) != want {
			return errors.New("agent: declared native resource limit was not applied")
		}
	}
	return nil
}
