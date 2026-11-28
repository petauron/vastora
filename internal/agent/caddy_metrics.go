package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const caddyAdminMetricsLimit = 4 << 20

// readAdminMetricsGeneration queries Caddy's Unix Admin /metrics endpoint,
// whose "received request" log is emitted at DEBUG in Caddy 2.11.4 rather than
// INFO. Transport failures are returned in transportErr so unreachable sockets
// fail immediately; non-200 or invalid/missing metrics return ok=false so the
// caller fails closed to a strict GET /config/ check.
func (driver *CaddyGatewayDriver) readAdminMetricsGeneration(ctx context.Context) (generation string, ok bool, transportErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, driver.AdminURL+"/metrics", nil)
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := driver.client().Do(request)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, caddyAdminMetricsLimit+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > caddyAdminMetricsLimit {
		return "", false, nil
	}
	generation, ok = parseCaddyAdminMetricsGeneration(raw)
	return generation, ok, nil
}

// parseCaddyAdminMetricsGeneration builds a canonical generation fingerprint
// from Caddy's Prometheus metrics exposition. It requires an explicit
// caddy_config_last_reload_successful=1 gauge and positive finite reload
// timestamp, plus a digest of all non-read Admin request/error counters (and
// process_start_time_seconds when present) so external writes, failed reloads,
// same-timestamp reloads, or process restarts change the fingerprint.
func parseCaddyAdminMetricsGeneration(raw []byte) (string, bool) {
	var (
		sawReloadSuccessful bool
		sawReloadTimestamp  bool
		reloadTimestampBits uint64
		sawProcessStart     bool
		processStartBits    uint64
		mutatingSamples     []string
		seenAdminSeries     = map[string]bool{}
	)
	for len(raw) > 0 {
		var lineBytes []byte
		if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
			lineBytes = raw[:idx]
			raw = raw[idx+1:]
		} else {
			lineBytes = raw
			raw = nil
		}
		line := strings.TrimSpace(string(lineBytes))
		if line == "" || line[0] == '#' {
			continue
		}
		name, rest, ok := scanPrometheusMetricName(line)
		if !ok {
			return "", false
		}
		switch name {
		case "caddy_config_last_reload_successful",
			"caddy_config_last_reload_success_timestamp_seconds",
			"caddy_admin_http_requests_total",
			"caddy_admin_http_request_errors_total",
			"process_start_time_seconds":
		default:
			continue
		}
		var labels map[string]string
		if len(rest) > 0 && rest[0] == '{' {
			labels, rest, ok = parsePrometheusLabels(rest)
			if !ok {
				return "", false
			}
		}
		if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
			return "", false
		}
		fields := strings.Fields(rest)
		if len(fields) != 1 && len(fields) != 2 {
			return "", false
		}
		if len(fields) == 2 {
			if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
				return "", false
			}
		}
		val, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			return "", false
		}
		switch name {
		case "caddy_config_last_reload_successful":
			if len(labels) != 0 || sawReloadSuccessful || val != 1 {
				return "", false
			}
			sawReloadSuccessful = true
		case "caddy_config_last_reload_success_timestamp_seconds":
			if len(labels) != 0 || sawReloadTimestamp || val <= 0 {
				return "", false
			}
			sawReloadTimestamp = true
			reloadTimestampBits = math.Float64bits(val)
		case "process_start_time_seconds":
			if len(labels) != 0 || sawProcessStart || val <= 0 {
				return "", false
			}
			sawProcessStart = true
			processStartBits = math.Float64bits(val)
		case "caddy_admin_http_requests_total":
			if val < 0 {
				return "", false
			}
			// Ignore only read-only GET series so our own /metrics scrapes and
			// strict /config/ readbacks do not perturb the config generation.
			if labels["method"] == http.MethodGet && (labels["path"] == "/metrics" || labels["path"] == "/config/" || labels["path"] == "/id/") {
				continue
			}
			seriesKey, sample := canonicalPrometheusSample(name, labels, val)
			if seenAdminSeries[seriesKey] {
				return "", false
			}
			seenAdminSeries[seriesKey] = true
			mutatingSamples = append(mutatingSamples, sample)
		case "caddy_admin_http_request_errors_total":
			if val < 0 {
				return "", false
			}
			seriesKey, sample := canonicalPrometheusSample(name, labels, val)
			if seenAdminSeries[seriesKey] {
				return "", false
			}
			seenAdminSeries[seriesKey] = true
			mutatingSamples = append(mutatingSamples, sample)
		}
	}
	if !sawReloadSuccessful || !sawReloadTimestamp {
		return "", false
	}
	sort.Strings(mutatingSamples)
	hasher := sha256.New()
	for _, sample := range mutatingSamples {
		_, _ = io.WriteString(hasher, sample)
		_, _ = hasher.Write([]byte{'\n'})
	}
	proc := "none"
	if sawProcessStart {
		proc = fmt.Sprintf("%016x", processStartBits)
	}
	return fmt.Sprintf("v1:reload=%016x:proc=%s:admin=%s", reloadTimestampBits, proc, hex.EncodeToString(hasher.Sum(nil))), true
}

func scanPrometheusMetricName(line string) (string, string, bool) {
	if len(line) == 0 {
		return "", "", false
	}
	c0 := line[0]
	if !((c0 >= 'a' && c0 <= 'z') || (c0 >= 'A' && c0 <= 'Z') || c0 == '_' || c0 == ':') {
		return "", "", false
	}
	i := 1
	for i < len(line) {
		c := line[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == ':' {
			i++
			continue
		}
		break
	}
	return line[:i], line[i:], true
}

func parsePrometheusLabels(input string) (map[string]string, string, bool) {
	if len(input) == 0 || input[0] != '{' {
		return nil, "", false
	}
	labels := map[string]string{}
	i := 1
	skipSpace := func() {
		for i < len(input) && (input[i] == ' ' || input[i] == '\t') {
			i++
		}
	}
	skipSpace()
	for i < len(input) {
		if input[i] == '}' {
			return labels, input[i+1:], true
		}
		keyStart := i
		if !((input[i] >= 'a' && input[i] <= 'z') || (input[i] >= 'A' && input[i] <= 'Z') || input[i] == '_') {
			return nil, "", false
		}
		i++
		for i < len(input) {
			c := input[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
				i++
				continue
			}
			break
		}
		key := input[keyStart:i]
		skipSpace()
		if i >= len(input) || input[i] != '=' {
			return nil, "", false
		}
		i++
		skipSpace()
		if i >= len(input) || input[i] != '"' {
			return nil, "", false
		}
		i++
		var val strings.Builder
		closed := false
		for i < len(input) {
			c := input[i]
			if c == '"' {
				i++
				closed = true
				break
			}
			if c == '\\' {
				i++
				if i >= len(input) {
					return nil, "", false
				}
				switch input[i] {
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				case 'n':
					val.WriteByte('\n')
				default:
					return nil, "", false
				}
				i++
				continue
			}
			if c == '\n' {
				return nil, "", false
			}
			val.WriteByte(c)
			i++
		}
		if !closed {
			return nil, "", false
		}
		if _, dup := labels[key]; dup {
			return nil, "", false
		}
		labels[key] = val.String()
		skipSpace()
		if i >= len(input) {
			return nil, "", false
		}
		if input[i] == ',' {
			i++
			skipSpace()
			continue
		}
		if input[i] == '}' {
			return labels, input[i+1:], true
		}
		return nil, "", false
	}
	return nil, "", false
}

func canonicalPrometheusSample(name string, labels map[string]string, val float64) (seriesKey, sample string) {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, key := range keys {
		b.WriteByte('|')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(labels[key]))
	}
	seriesKey = b.String()
	// Normalize -0.0 to +0.0 before bit encoding.
	if val == 0 {
		val = 0
	}
	return seriesKey, fmt.Sprintf("%s|%016x", seriesKey, math.Float64bits(val))
}
