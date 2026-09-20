package xrayrecovery

import (
	"errors"
	"strings"
)

const (
	InspectKind    = "xray.configuration.inspect"
	ApplyKind      = "xray.configuration.apply"
	RuntimeSource  = "runtime"
	AgentSource    = "agent_state"
	MaxResultBytes = 64 << 10
)

type Task struct {
	Action                string `json:"action"`
	ApplicationID         string `json:"applicationId"`
	ExpectedRuntimeSHA256 string `json:"expectedRuntimeSha256,omitempty"`
	ExpectedAgentSHA256   string `json:"expectedAgentSha256,omitempty"`
	ExpectedAgentRevision uint64 `json:"expectedAgentRevision,omitempty"`
}

func (t Task) Validate(kind string) error {
	if strings.TrimSpace(t.ApplicationID) == "" {
		return errors.New("xray recovery: application identity is required")
	}
	if kind == InspectKind && t.Action == "inspect" && t.ExpectedRuntimeSHA256 == "" && t.ExpectedAgentSHA256 == "" && t.ExpectedAgentRevision == 0 {
		return nil
	}
	if kind != ApplyKind || t.Action != RuntimeSource && t.Action != AgentSource || !validDigest(t.ExpectedRuntimeSHA256) || !validDigest(t.ExpectedAgentSHA256) || t.ExpectedAgentRevision == 0 {
		return errors.New("xray recovery: invalid apply task")
	}
	return nil
}

type InboundSummary struct {
	Tag      string `json:"tag"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Clients  int    `json:"clients"`
}

type Difference struct {
	Kind    string `json:"kind"`
	Inbound string `json:"inbound,omitempty"`
	Field   string `json:"field,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	Agent   string `json:"agent,omitempty"`
}

type Result struct {
	Action            string           `json:"action"`
	ApplicationID     string           `json:"applicationId"`
	RuntimeSHA256     string           `json:"runtimeSha256"`
	AgentSHA256       string           `json:"agentSha256"`
	AgentRevision     uint64           `json:"agentRevision"`
	RuntimeImportable bool             `json:"runtimeImportable"`
	Matches           bool             `json:"matches"`
	RuntimeInbounds   []InboundSummary `json:"runtimeInbounds"`
	AgentInbounds     []InboundSummary `json:"agentInbounds"`
	Differences       []Difference     `json:"differences"`
	AppliedSource     string           `json:"appliedSource,omitempty"`
}

func (r Result) Validate(kind string) error {
	if strings.TrimSpace(r.ApplicationID) == "" || !validDigest(r.RuntimeSHA256) || !validDigest(r.AgentSHA256) || r.AgentRevision == 0 || len(r.Differences) > 128 || len(r.RuntimeInbounds) > 64 || len(r.AgentInbounds) > 64 {
		return errors.New("xray recovery: invalid result")
	}
	if kind == InspectKind && r.Action == "inspect" && r.AppliedSource == "" {
		return nil
	}
	if kind != ApplyKind || r.Action != RuntimeSource && r.Action != AgentSource || r.AppliedSource != r.Action || !r.Matches {
		return errors.New("xray recovery: invalid applied result")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
