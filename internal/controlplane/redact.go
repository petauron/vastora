package controlplane

import (
	"regexp"
	"strings"
)

var controlPlaneSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:["']?(?:access[_ -]?token|auth[_ -]?key|api[_ -]?key|credential|token|password|secret|private[_ -]?key|session|cookie)["']?)\s*[:=]\s*["']?)[^"',}\s;]+`),
	regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`),
	regexp.MustCompile(`([?&][^=\s&]+=)([^&\s]+)`),
	regexp.MustCompile(`-----BEGIN [^-]+-----.*?-----END [^-]+-----`),
	regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`),
}

// SafeError keeps short operational context while removing credential-shaped
// material before an error crosses or is persisted by the control plane.
func SafeError(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	for _, pattern := range controlPlaneSecretPatterns {
		value = pattern.ReplaceAllString(value, `${1}[redacted]`)
	}
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
