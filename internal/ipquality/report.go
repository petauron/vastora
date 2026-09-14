// Package ipquality defines the bounded, public subset of an IPQuality report.
package ipquality

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
)

const Kind = "node.ip-quality"
const MaxReportBytes = 64 * 1024

type Task struct {
	Address     string `json:"address"`
	BindAddress string `json:"bindAddress"`
}

func (t Task) Validate() error {
	ip, bind := net.ParseIP(t.Address), net.ParseIP(t.BindAddress)
	if ip == nil || bind == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.To4() == nil) != (bind.To4() == nil) {
		return errors.New("ipquality: invalid node address")
	}
	return nil
}

type Score struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}
type Service struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	RegionCode string `json:"regionCode,omitempty"`
	Type       string `json:"type,omitempty"`
}
type Report struct {
	Address    string    `json:"address"`
	Version    string    `json:"version"`
	RegionCode string    `json:"regionCode,omitempty"`
	Scores     []Score   `json:"scores"`
	Services   []Service `json:"services"`
}

// A probe failure is a completed diagnostic, not an unresolved configuration
// mutation. Error codes never contain raw upstream output, URLs or credentials.
type Result struct {
	Report *Report `json:"report,omitempty"`
	Error  string  `json:"error,omitempty"`
}

var sources = []string{"IP2LOCATION", "SCAMALYTICS", "ipapi", "AbuseIPDB", "IPQS", "DBIP"}
var services = []string{"TikTok", "DisneyPlus", "Netflix", "Youtube", "AmazonPrimeVideo", "Reddit", "ChatGPT"}
var scorePattern = regexp.MustCompile(`^\d{1,3}(\.\d{1,6})?%?$`)
var regionPattern = regexp.MustCompile(`^[A-Z]{2}$`)

func (r Result) Validate(address string) error {
	if r.Error != "" {
		switch r.Error {
		case "docker_unavailable", "download_failed", "detection_failed", "timeout", "ip_changed", "invalid_report":
			if r.Report == nil {
				return nil
			}
		}
		return errors.New("ipquality: invalid diagnostic error")
	}
	if r.Report == nil || net.ParseIP(address) == nil || !net.ParseIP(address).Equal(net.ParseIP(r.Report.Address)) || len(r.Report.Version) > 40 || r.Report.Version == "" || len(r.Report.Scores) > len(sources) || len(r.Report.Services) > len(services) {
		return errors.New("ipquality: invalid report identity")
	}
	if r.Report.Scores == nil || r.Report.Services == nil || len(r.Report.Scores)+len(r.Report.Services) == 0 {
		return errors.New("ipquality: empty report")
	}
	encoded, err := json.Marshal(r.Report)
	if err != nil || len(encoded) > MaxReportBytes {
		return errors.New("ipquality: report too large")
	}
	seen := make(map[string]bool)
	for _, score := range r.Report.Scores {
		if seen[score.Source] || !contains(sources, score.Source) || !scorePattern.MatchString(score.Value) {
			return errors.New("ipquality: invalid score")
		}
		seen[score.Source] = true
	}
	for _, service := range r.Report.Services {
		if seen[service.Name] || !contains(services, service.Name) || len(service.Status) > 32 || len(service.Type) > 32 || service.RegionCode != "" && !regionPattern.MatchString(service.RegionCode) {
			return errors.New("ipquality: invalid service")
		}
		seen[service.Name] = true
	}
	if r.Report.RegionCode != "" && !regionPattern.MatchString(r.Report.RegionCode) {
		return errors.New("ipquality: invalid region")
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Parse retains no raw report, coordinates, report link, command or provider
// response. Missing scores remain absent, never converted into zero risk.
func Parse(raw []byte, address string) (Report, error) {
	if len(raw) > MaxReportBytes {
		return Report{}, errors.New("invalid_report")
	}
	start, end := bytes.IndexByte(raw, '{'), bytes.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return Report{}, errors.New("invalid_report")
	}
	var input struct {
		Head  struct{ IP, Version string }
		Info  struct{ Region struct{ Code string } }
		Score map[string]json.RawMessage
		Media map[string]struct{ Status, Region, Type string }
	}
	if json.Unmarshal(raw[start:end+1], &input) != nil {
		return Report{}, errors.New("invalid_report")
	}
	ip := net.ParseIP(input.Head.IP)
	if ip == nil || !ip.Equal(net.ParseIP(address)) {
		return Report{}, errors.New("ip_changed")
	}
	report := Report{Address: ip.String(), Version: input.Head.Version, RegionCode: region(input.Info.Region.Code), Scores: []Score{}, Services: []Service{}}
	for _, source := range sources {
		rawValue := input.Score[source]
		value := strings.TrimSpace(strings.Trim(string(rawValue), `"`))
		if scorePattern.MatchString(value) {
			report.Scores = append(report.Scores, Score{Source: source, Value: value})
		}
	}
	for _, name := range services {
		if item, ok := input.Media[name]; ok {
			report.Services = append(report.Services, Service{Name: name, Status: strings.TrimSpace(item.Status), RegionCode: region(item.Region), Type: strings.TrimSpace(item.Type)})
		}
	}
	if (Result{Report: &report}).Validate(address) != nil || len(report.Scores)+len(report.Services) == 0 {
		return Report{}, errors.New("invalid_report")
	}
	return report, nil
}

func region(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if !regionPattern.MatchString(value) {
		return ""
	}
	return value
}
