package senulun

import (
	"errors"
	"strings"
)

// MigrateEnvironment converts an inspected legacy installation once, before
// starting its replacement. It is not a second runtime configuration reader.
// Values are preserved verbatim; only a known mounted data prefix is moved.
func MigrateEnvironment(entries []string) ([]string, error) {
	if len(entries) > 256 {
		return nil, errors.New("senulun: environment exceeds migration bound")
	}
	seen := make(map[string]bool, len(entries))
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry) > 16384 || strings.ContainsAny(entry, "\x00\r\n") {
			return nil, errors.New("senulun: unsafe environment assignment")
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, errors.New("senulun: invalid environment assignment")
		}
		if strings.HasPrefix(key, "PULSE_") {
			key = "SENULUN_" + strings.TrimPrefix(key, "PULSE_")
		}
		if seen[key] {
			return nil, errors.New("senulun: conflicting old and new environment assignments")
		}
		seen[key] = true
		if strings.HasPrefix(key, "SENULUN_") && strings.HasPrefix(value, "/var/lib/pulse/") {
			value = "/var/lib/senulun/" + strings.TrimPrefix(value, "/var/lib/pulse/")
		}
		result = append(result, key+"="+value)
	}
	return result, nil
}
