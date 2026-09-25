package ipquality

import (
	"errors"
	"math"
	"net"
	"strings"
	"time"
)

// Official endpoint and direction: https://ippure.com/MyIP-Info-API and
// https://ippure.com/faq (verified 2026-09-25). The API reports the caller's IP.
const IPPureProvider = "https://my.ippure.com/v1/info"

type Observation struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	Address   string `json:"address"`
	CheckedAt string `json:"checkedAt"`
}

type IPPureResult struct {
	Provider    string   `json:"provider"`
	Status      string   `json:"status"`
	Address     string   `json:"address,omitempty"`
	CheckedAt   string   `json:"checkedAt"`
	RiskScore   *float64 `json:"riskScore,omitempty"`
	Residential *bool    `json:"residential,omitempty"`
	Broadcast   *bool    `json:"broadcast,omitempty"`
}

// Capture the batch acquisition time, not a provider's undocumented database
// update time. Missing upstream values remain missing; none become clean/zero.
func (r *Report) RecordObservations(now time.Time) {
	names := append([]string{}, sources...)
	for _, name := range classificationSources {
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range services {
		names = append(names, "service:"+name)
	}
	r.Observations = make([]Observation, 0, len(names))
	for _, name := range names {
		status := "missing"
		for _, score := range r.Scores {
			if score.Source == name {
				status = "ok"
			}
		}
		for _, value := range r.UsageTypes {
			if value.Source == name {
				status = "ok"
			}
		}
		for _, service := range r.Services {
			if "service:"+service.Name == name {
				switch strings.ToLower(service.Status) {
				case "yes", "no", "org", "originals only":
					status = "ok"
				}
			}
		}
		r.Observations = append(r.Observations, Observation{Source: name, Status: status, Address: r.Address, CheckedAt: now.UTC().Format(time.RFC3339Nano)})
	}
}

func (r Report) validateEvidence() error {
	if len(r.Observations) > len(sources)+len(classificationSources)+len(services) {
		return errors.New("ipquality: too many observations")
	}
	seen := map[string]bool{}
	for _, item := range r.Observations {
		validSource := contains(sources, item.Source) || contains(classificationSources, item.Source) || strings.HasPrefix(item.Source, "service:") && contains(services, strings.TrimPrefix(item.Source, "service:"))
		_, timeErr := time.Parse(time.RFC3339Nano, item.CheckedAt)
		if !validSource || seen[item.Source] || !sameIP(item.Address, r.Address) || timeErr != nil || item.Status != "ok" && item.Status != "missing" {
			return errors.New("ipquality: invalid observation")
		}
		seen[item.Source] = true
	}
	if p := r.IPPure; p != nil {
		_, timeErr := time.Parse(time.RFC3339Nano, p.CheckedAt)
		if p.Provider != IPPureProvider || timeErr != nil {
			return errors.New("ipquality: invalid IPPure provenance")
		}
		switch p.Status {
		case "ok":
			if !sameIP(p.Address, r.Address) || net.ParseIP(p.Address).To4() == nil || p.RiskScore == nil || math.IsNaN(*p.RiskScore) || math.IsInf(*p.RiskScore, 0) || *p.RiskScore < 0 || *p.RiskScore > 100 {
				return errors.New("ipquality: invalid IPPure result")
			}
		case "unavailable", "invalid_response", "ip_mismatch", "unsupported":
			if p.RiskScore != nil || p.Residential != nil || p.Broadcast != nil || p.Address != "" && net.ParseIP(p.Address) == nil {
				return errors.New("ipquality: invalid IPPure failure")
			}
		default:
			return errors.New("ipquality: invalid IPPure status")
		}
	}
	return nil
}
