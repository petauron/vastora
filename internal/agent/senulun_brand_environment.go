package agent

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/petauron/vastora/internal/senulun"
)

func senulunCanonicalEnvironment(environment []string) bool {
	for _, entry := range environment {
		if strings.HasPrefix(entry, "SENULUN_PUBLIC_URL=") {
			return true
		}
	}
	return false
}

func (b *SystemdPackageBackend) preserveSenulunEnvironment(task DeploymentTask, receipt *InstanceResources, path string) error {
	if task.AppKey != senulun.AgentKey || task.Operation != "upgrade" || task.Manifest.Runtime.Systemd.Executable != "senulun-agent" {
		return nil
	}
	old := resourceNamed(receipt, "file", "environment")
	if old == nil {
		return errors.New("agent: Senulun environment ownership unavailable")
	}
	raw, err := os.ReadFile(b.Manager.path(old.Path))
	if err != nil {
		return err
	}
	entries, err := senulunEnvironmentEntries(raw)
	if err != nil {
		return err
	}
	converted, err := senulun.MigrateEnvironment(entries)
	if err != nil {
		return err
	}
	current, err := senulunEnvironmentEntries(b.files[path])
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	for _, entry := range current {
		key, _, _ := strings.Cut(entry, "=")
		keys[key] = true
	}
	var appended strings.Builder
	for _, entry := range converted {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "SENULUN_") && !keys[key] {
			appended.WriteString(key + "=" + strconv.Quote(value) + "\n")
		}
	}
	b.files[path] = append(b.files[path], []byte(appended.String())...)
	return nil
}

func senulunEnvironmentEntries(raw []byte) ([]string, error) {
	if len(raw) > 64<<10 {
		return nil, errors.New("agent: Senulun environment exceeds migration limit")
	}
	var entries []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("agent: unsupported Senulun environment syntax")
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			var err error
			value, err = strconv.Unquote(value)
			if err != nil {
				return nil, errors.New("agent: invalid Senulun environment quoting")
			}
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") || strings.Contains(value[1:len(value)-1], "'") {
				return nil, errors.New("agent: invalid Senulun environment quoting")
			}
			value = value[1 : len(value)-1]
		}
		entries = append(entries, strings.TrimSpace(key)+"="+value)
	}
	return entries, nil
}

func senulunUpgradeEnvironment(previous, desired []string) ([]string, error) {
	converted, err := senulun.MigrateEnvironment(previous)
	if err != nil {
		return nil, err
	}
	for i, entry := range converted {
		if strings.HasPrefix(entry, "SENULUN_DATABASE_PATH=") {
			if entry != "SENULUN_DATABASE_PATH=/var/lib/senulun/pulse.db" && entry != "SENULUN_DATABASE_PATH=/var/lib/senulun/store/senulun.db" {
				return nil, errors.New("agent: custom Senulun database requires explicit offline migration; existing configuration is unchanged")
			}
			converted[i] = "SENULUN_DATABASE_PATH=/var/lib/senulun/store/senulun.db"
		}
	}
	// Preserve all existing choices, including OAuth, limits and the traffic
	// credential key. Only the current managed public origin/token path are
	// authoritative replacements. Nothing is printed or persisted to Center.
	for _, assignment := range desired {
		key, _, _ := strings.Cut(assignment, "=")
		found := false
		for i, entry := range converted {
			if strings.HasPrefix(entry, key+"=") {
				converted[i], found = assignment, true
			}
		}
		if !found {
			converted = append(converted, assignment)
		}
	}
	return converted, nil
}
