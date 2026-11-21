//go:build linux

package osreinstall

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/petauron/vastora/internal/hostservice"
)

// verifyUnconditionalSSHConfig refuses conditional or externally included
// server configuration. Without a known management source, checking just one
// sshd -C address would not prove that another Match block is safe.
func verifyUnconditionalSSHConfig() (int64, error) {
	const root = "/etc/ssh"
	seen := map[string]bool{}
	var newest int64
	noteChange := func(info os.FileInfo) error {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("reinstall: SSH configuration change time is unavailable")
		}
		if info.ModTime().Unix() > newest {
			newest = info.ModTime().Unix()
		}
		// Restoring an archive can preserve an old mtime even though sshd has
		// not loaded the restored contents. ctime records the local change.
		if stat.Ctim.Sec > newest {
			newest = stat.Ctim.Sec
		}
		return nil
	}
	var scan func(string, int) error
	scan = func(name string, depth int) error {
		name = filepath.Clean(name)
		if depth > 8 || len(seen) >= 128 || !strings.HasPrefix(name, root+"/") {
			return errors.New("reinstall: SSH include requires review")
		}
		if seen[name] {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil || resolved != name {
			return errors.New("reinstall: SSH configuration link requires review")
		}
		seen[name] = true
		data, err := readOwnedAccessFile(name, 0, 1<<20)
		if err != nil {
			return errors.New("reinstall: SSH configuration is unavailable or unsafe")
		}
		info, err := os.Stat(name)
		if err != nil {
			return errors.New("reinstall: SSH configuration is unavailable or unsafe")
		}
		if err = noteChange(info); err != nil {
			return err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			end := strings.IndexAny(line, " \t=")
			if end < 0 {
				continue
			}
			key := strings.ToLower(line[:end])
			args := strings.TrimSpace(line[end:])
			args = strings.TrimSpace(strings.TrimPrefix(args, "="))
			switch key {
			case "match":
				return errors.New("reinstall: conditional SSH Match rules require explicit review")
			case "include":
				fields := strings.Fields(args)
				if len(fields) == 0 || strings.Contains(args, "#") {
					return errors.New("reinstall: invalid SSH include")
				}
				for _, pattern := range fields {
					// Go and OpenSSH differ on some bracket-glob forms; reject
					// those rather than miss a server-side Include or Match.
					if strings.ContainsAny(pattern, "\\\"'~%[]") {
						return errors.New("reinstall: SSH include requires review")
					}
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(root, pattern)
					}
					pattern = filepath.Clean(pattern)
					if !strings.HasPrefix(pattern, root+"/") {
						return errors.New("reinstall: external SSH include requires review")
					}
					// Directory metadata is needed to catch a deleted include: the
					// removed file no longer exists to supply its own change time.
					dir := filepath.Dir(pattern)
					if strings.ContainsAny(dir, "*?") {
						return errors.New("reinstall: SSH include directory requires review")
					}
					resolved, err := filepath.EvalSymlinks(dir)
					if err != nil || resolved != dir {
						return errors.New("reinstall: SSH include directory requires review")
					}
					info, err := os.Lstat(dir)
					if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
						return errors.New("reinstall: SSH include directory requires review")
					}
					stat, ok := info.Sys().(*syscall.Stat_t)
					if !ok || stat.Uid != 0 || stat.Gid != 0 {
						return errors.New("reinstall: SSH include directory requires review")
					}
					if err = noteChange(info); err != nil {
						return err
					}
					matches, err := filepath.Glob(pattern)
					if err != nil {
						return errors.New("reinstall: invalid SSH include")
					}
					for _, match := range matches {
						if err := scan(match, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		return scanner.Err()
	}
	if err := scan(filepath.Join(root, "sshd_config"), 0); err != nil {
		return 0, err
	}
	return newest, nil
}

func verifySSHPolicy(ctx context.Context, username string, port int) (int64, error) {
	if !safeUser.MatchString(username) || port < 1 || port > 65535 {
		return 0, errors.New("reinstall: invalid SSH account or port")
	}
	newest, err := verifyUnconditionalSSHConfig()
	if err != nil {
		return 0, err
	}
	if _, err := readCommand(ctx, "/usr/sbin/sshd", "-t"); err != nil {
		return 0, errors.New("reinstall: SSH configuration is invalid")
	}
	data, err := readCommand(ctx, "/usr/sbin/sshd", "-T", "-C", fmt.Sprintf("user=%s,host=localhost,addr=127.0.0.1", username))
	if err != nil {
		return 0, errors.New("reinstall: SSH effective configuration is unavailable")
	}
	props := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		v := strings.Fields(line)
		if len(v) > 1 {
			props[v[0]] = append(props[v[0]], strings.Join(v[1:], " "))
		}
	}
	single := func(k, v string) bool { return len(props[k]) == 1 && props[k][0] == v }
	if !single("port", strconv.Itoa(port)) || !single("pubkeyauthentication", "yes") ||
		!single("passwordauthentication", "no") || !single("kbdinteractiveauthentication", "no") ||
		!single("authenticationmethods", "publickey") || !single("authorizedkeyscommand", "none") ||
		!single("trustedusercakeys", "none") || !single("forcecommand", "none") ||
		!single("authorizedkeysfile", ".ssh/authorized_keys .ssh/authorized_keys2") {
		return 0, errors.New("reinstall: SSH must use the reviewed port and explicit publickey-only authentication")
	}
	if username != "root" && !single("permitrootlogin", "no") {
		return 0, errors.New("reinstall: root SSH login must be disabled for a non-root maintenance account")
	}
	if username == "root" && !single("permitrootlogin", "yes") && !single("permitrootlogin", "without-password") && !single("permitrootlogin", "prohibit-password") {
		return 0, errors.New("reinstall: SSH root maintenance account is not permitted")
	}
	for _, directive := range []string{"allowusers", "denyusers", "allowgroups", "denygroups"} {
		if len(props[directive]) != 0 {
			return 0, errors.New("reinstall: SSH account access restrictions require review")
		}
	}
	return newest, nil
}

func systemdProperty(ctx context.Context, unit, property string) (string, error) {
	data, err := readCommand(ctx, "systemctl", "show", unit, "-p", property, "--value")
	if err != nil {
		return "", errors.New("reinstall: service state is unavailable")
	}
	return strings.TrimSpace(string(data)), nil
}

// Verify that the running SSH listener uses the standard config, without
// command-line -f/-o overrides that sshd -T on its own would miss.
func verifySSHService(ctx context.Context, port int, newestConfig int64) error {
	for property, expected := range map[string]string{"ActiveState": "active", "LoadState": "loaded", "NeedDaemonReload": "no"} {
		actual, err := systemdProperty(ctx, "ssh.service", property)
		if err != nil || actual != expected {
			return fmt.Errorf("reinstall: SSH service %s requires review", property)
		}
	}
	stamp, err := readCommand(ctx, "systemctl", "show", "ssh.service", "-p", "StateChangeTimestamp", "--value", "--timestamp=unix")
	if err != nil {
		return errors.New("reinstall: SSH service load time is unavailable")
	}
	loadedAt, parseErr := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(stamp)), "@"), 10, 64)
	if parseErr != nil || loadedAt <= newestConfig {
		return errors.New("reinstall: SSH configuration changed since the service was started or reloaded")
	}
	reload, err := systemdProperty(ctx, "ssh.service", "ReloadResult")
	if err != nil || reload != "success" {
		return errors.New("reinstall: SSH service reload requires review")
	}
	value, err := systemdProperty(ctx, "ssh.service", "MainPID")
	pid, parseErr := strconv.Atoi(value)
	if err != nil || parseErr != nil || pid <= 1 {
		return errors.New("reinstall: SSH listener identity is unavailable")
	}
	exe, err := filepath.EvalSymlinks(filepath.Join("/proc", value, "exe"))
	if err != nil || exe != "/usr/sbin/sshd" {
		return errors.New("reinstall: SSH listener executable requires review")
	}
	raw, err := os.ReadFile(filepath.Join("/proc", value, "cmdline"))
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return errors.New("reinstall: SSH listener arguments are unavailable")
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	plain := len(args) == 2 && args[0] == "/usr/sbin/sshd" && args[1] == "-D"
	title := len(args) == 1 && strings.HasPrefix(args[0], "sshd: /usr/sbin/sshd -D [listener] ")
	if !plain && !title {
		return errors.New("reinstall: SSH listener arguments require review")
	}
	listeners, err := readCommand(ctx, "ss", "-H", "-ltnp")
	if err != nil {
		return errors.New("reinstall: SSH listening sockets are unavailable")
	}
	matched := 0
	for _, line := range strings.Split(string(listeners), "\n") {
		if !strings.Contains(line, "pid="+value+",") && !strings.Contains(line, "pid="+value+")") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return errors.New("reinstall: SSH listening socket requires review")
		}
		_, actualPort, e := net.SplitHostPort(fields[3])
		if e != nil || actualPort != strconv.Itoa(port) {
			return errors.New("reinstall: SSH listener port differs from the reviewed port")
		}
		matched++
	}
	if matched == 0 {
		return errors.New("reinstall: SSH listener for the reviewed port is unavailable")
	}
	return nil
}

func verifyAgentService(ctx context.Context) ([]byte, error) {
	const unitPath = "/etc/systemd/system/vastora-agent.service"
	unit, err := readOwnedAccessFile(unitPath, 0, 16<<10)
	if err != nil {
		return nil, errors.New("reinstall: Agent service unit is unavailable or unsafe")
	}
	roles, capabilities, err := hostservice.ParseRestoredAgentUnit(unit)
	if err != nil {
		return nil, err
	}
	for property, expected := range map[string]string{
		"FragmentPath": unitPath, "DropInPaths": "", "LoadState": "loaded",
		"ActiveState": "active", "UnitFileState": "enabled", "NeedDaemonReload": "no",
		"Type": "simple", "Restart": "always", "RestartUSec": "5s", "TimeoutStopUSec": "30s",
		"LogRateLimitIntervalUSec": "30s", "LogRateLimitBurst": "100",
	} {
		actual, e := systemdProperty(ctx, "vastora-agent.service", property)
		if e != nil || actual != expected {
			return nil, fmt.Errorf("reinstall: Agent service %s requires review", property)
		}
	}
	start, err := systemdProperty(ctx, "vastora-agent.service", "ExecStart")
	expectedStart := "{ path=/usr/local/bin/vastora ; argv[]=/usr/local/bin/vastora agent serve --data-dir /var/lib/vastora/agent --roles " + roles + " --capabilities " + capabilities + " ; ignore_errors=no ; "
	if err != nil || !strings.HasPrefix(start, expectedStart) {
		return nil, errors.New("reinstall: running Agent service arguments require review")
	}
	wanted, err := systemdProperty(ctx, "vastora-agent.service", "Wants")
	if err != nil || !containsWord(wanted, "network-online.target") {
		return nil, errors.New("reinstall: running Agent service dependencies require review")
	}
	after, err := systemdProperty(ctx, "vastora-agent.service", "After")
	dependency := "docker.service"
	if capabilities == "landing" {
		dependency = "tailscaled.service"
	}
	if err != nil || !containsWord(after, "network-online.target") || !containsWord(after, dependency) {
		return nil, errors.New("reinstall: running Agent service dependencies require review")
	}
	return unit, nil
}

func containsWord(value, wanted string) bool {
	for _, item := range strings.Fields(value) {
		if item == wanted {
			return true
		}
	}
	return false
}

func reviewedSSHKeys(username string) ([]byte, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return nil, errors.New("reinstall: SSH maintenance account is unavailable")
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	home := filepath.Clean(account.HomeDir)
	if err != nil || username == "root" && home != "/root" || username != "root" && home != "/home/"+username {
		return nil, errors.New("reinstall: SSH account home requires review")
	}
	for _, directory := range []string{home, filepath.Join(home, ".ssh")} {
		info, e := os.Lstat(directory)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("reinstall: SSH account directory requires review")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != uint32(uid) {
			return nil, errors.New("reinstall: SSH account directory requires review")
		}
	}
	keys, err := readOwnedAccessFile(filepath.Join(home, ".ssh/authorized_keys"), uint32(uid), 64<<10)
	if err != nil || !validPublicKeys(keys) {
		return nil, errors.New("reinstall: SSH authorized keys require review")
	}
	if _, err := os.Lstat(filepath.Join(home, ".ssh/authorized_keys2")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("reinstall: additional SSH authorized keys require review")
	}
	return keys, nil
}

// VerifyRestoredOnboarding checks only host configuration and existing public
// keys; it does not change or open the Agent, subscription or traffic state.
func VerifyRestoredOnboarding(ctx context.Context, username string, port int) (OnboardingEvidence, error) {
	if os.Geteuid() != 0 {
		return OnboardingEvidence{}, errors.New("reinstall: onboarding verification requires root")
	}
	newest, err := verifySSHPolicy(ctx, username, port)
	if err != nil {
		return OnboardingEvidence{}, err
	}
	if err := verifySSHService(ctx, port, newest); err != nil {
		return OnboardingEvidence{}, err
	}
	keys, err := reviewedSSHKeys(username)
	if err != nil {
		return OnboardingEvidence{}, err
	}
	unit, err := verifyAgentService(ctx)
	if err != nil {
		return OnboardingEvidence{}, err
	}
	return OnboardingEvidence{Username: username, SSHPort: port, AuthorizedKeysSHA256: sha256Digest(keys), AgentUnitSHA256: sha256Digest(unit)}, nil
}
