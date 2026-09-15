// Package nodediagnostics defines bounded tasks and results for active node
// network diagnostics. It intentionally contains no credentials or raw tool
// output.
package nodediagnostics

import (
	"errors"
	"net"
	"regexp"
	"slices"
)

const (
	NetworkKind     = "node.network-quality"
	ReturnRouteKind = "node.return-route"
	BandwidthKind   = "node.international-bandwidth"
	MaxResultBytes  = 64 * 1024
	ProbeCount      = 4
	BandwidthBytes  = 8 * 1024 * 1024
)

type Target struct {
	Carrier string `json:"carrier"`
	Address string `json:"address"`
}

// CarrierTargets are migrated from the operator's existing Komari probe
// configuration. They are public, credential-free endpoints. Vastora owns this
// revision at runtime and does not read Komari state.
var CarrierTargets = []Target{
	{Carrier: "telecom", Address: "hb-ct-v4.ip.zstaticcdn.com:80"},
	{Carrier: "unicom", Address: "hb-cu-v4.ip.zstaticcdn.com:80"},
	{Carrier: "mobile", Address: "hb-cm-v4.ip.zstaticcdn.com:80"},
}

type BandwidthTarget struct {
	Region   string `json:"region"`
	Location string `json:"location"`
	Host     string `json:"host"`
}

// BandwidthTargets are public iPerf3 endpoints documented by Leaseweb. Tests
// are manual, sequential and byte-bounded because these are shared services,
// not a Vastora-owned capacity pool.
var BandwidthTargets = []BandwidthTarget{
	{Region: "apac", Location: "Singapore", Host: "speedtest.sin1.sg.leaseweb.net"},
	{Region: "north-america", Location: "Los Angeles", Host: "speedtest.lax12.us.leaseweb.net"},
	{Region: "europe", Location: "Frankfurt", Host: "speedtest.fra1.de.leaseweb.net"},
}

type Task struct {
	BindAddress      string            `json:"bindAddress"`
	Targets          []Target          `json:"targets,omitempty"`
	BandwidthTargets []BandwidthTarget `json:"bandwidthTargets,omitempty"`
}

type NetworkMeasurement struct {
	Carrier string  `json:"carrier"`
	Latency float64 `json:"latencyMs"`
	Jitter  float64 `json:"jitterMs"`
	Loss    float64 `json:"lossPercent"`
}

type Hop struct {
	TTL      int     `json:"ttl"`
	Address  string  `json:"address,omitempty"`
	Hostname string  `json:"hostname,omitempty"`
	Latency  float64 `json:"latencyMs,omitempty"`
}

type Route struct {
	Carrier    string `json:"carrier"`
	StopReason string `json:"stopReason"`
	Hops       []Hop  `json:"hops"`
}

type BandwidthMeasurement struct {
	Region    string  `json:"region"`
	Location  string  `json:"location"`
	Direction string  `json:"direction"`
	State     string  `json:"state"`
	Error     string  `json:"error,omitempty"`
	Megabits  float64 `json:"megabitsPerSecond"`
	Bytes     int64   `json:"bytes"`
	Duration  float64 `json:"durationSeconds"`
}

type Result struct {
	Network   []NetworkMeasurement   `json:"network,omitempty"`
	Routes    []Route                `json:"routes,omitempty"`
	Bandwidth []BandwidthMeasurement `json:"bandwidth,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

func ValidateBandwidthTargets(targets []BandwidthTarget) error {
	if !slices.Equal(targets, BandwidthTargets) {
		return errors.New("node diagnostics: invalid bandwidth targets")
	}
	return nil
}

var carrierPattern = regexp.MustCompile(`^(telecom|unicom|mobile)$`)

func (t Task) Validate() error {
	bind := net.ParseIP(t.BindAddress)
	if bind == nil || bind.To4() == nil || len(t.Targets) != 3 {
		return errors.New("node diagnostics: invalid task")
	}
	seen := map[string]bool{}
	for index, target := range t.Targets {
		host, port, err := net.SplitHostPort(target.Address)
		expected := CarrierTargets[index]
		if err != nil || host == "" || port != "80" || target != expected || !carrierPattern.MatchString(target.Carrier) || seen[target.Carrier] || len(target.Address) > 255 {
			return errors.New("node diagnostics: invalid target")
		}
		seen[target.Carrier] = true
	}
	return nil
}

func (t Task) ValidateBandwidth() error {
	bind := net.ParseIP(t.BindAddress)
	if bind == nil || bind.To4() == nil || len(t.Targets) != 0 {
		return errors.New("node diagnostics: invalid bandwidth task")
	}
	return ValidateBandwidthTargets(t.BandwidthTargets)
}

func (r Result) Validate(kind string) error {
	if r.Error != "" {
		if r.Error == "timeout" || r.Error == "probe_failed" || r.Error == "tool_unavailable" {
			return nil
		}
		return errors.New("node diagnostics: invalid error")
	}
	if kind == NetworkKind {
		if len(r.Network) != 3 || len(r.Routes) != 0 || len(r.Bandwidth) != 0 {
			return errors.New("node diagnostics: invalid network result")
		}
		seen := map[string]bool{}
		for _, value := range r.Network {
			if !carrierPattern.MatchString(value.Carrier) || seen[value.Carrier] || value.Latency < 0 || value.Latency > 60_000 || value.Jitter < 0 || value.Jitter > 60_000 || value.Loss < 0 || value.Loss > 100 {
				return errors.New("node diagnostics: invalid network measurement")
			}
			seen[value.Carrier] = true
		}
		return nil
	}
	if kind == ReturnRouteKind {
		if len(r.Routes) != 3 || len(r.Network) != 0 || len(r.Bandwidth) != 0 {
			return errors.New("node diagnostics: invalid route result")
		}
		seen := map[string]bool{}
		for _, route := range r.Routes {
			if !carrierPattern.MatchString(route.Carrier) || seen[route.Carrier] || len(route.StopReason) > 32 || len(route.Hops) > 30 {
				return errors.New("node diagnostics: invalid route")
			}
			seen[route.Carrier] = true
			for _, hop := range route.Hops {
				if hop.TTL < 1 || hop.TTL > 30 || len(hop.Address) > 64 || len(hop.Hostname) > 255 || hop.Latency < 0 || hop.Latency > 60_000 || hop.Address != "" && net.ParseIP(hop.Address) == nil {
					return errors.New("node diagnostics: invalid route hop")
				}
			}
		}
		return nil
	}
	if kind == BandwidthKind {
		if len(r.Bandwidth) != len(BandwidthTargets)*2 || len(r.Network) != 0 || len(r.Routes) != 0 {
			return errors.New("node diagnostics: invalid bandwidth result")
		}
		seen := map[string]bool{}
		for _, value := range r.Bandwidth {
			key := value.Region + "/" + value.Direction
			validState := value.State == "completed" && value.Error == "" && value.Bytes > 0 && value.Duration > 0 || value.State == "unavailable" && value.Error == "endpoint_busy" && value.Bytes == 0 && value.Duration == 0 && value.Megabits == 0 || value.State == "failed" && value.Error == "probe_failed" && value.Bytes == 0 && value.Duration == 0 && value.Megabits == 0
			if seen[key] || !validState || value.Direction != "upload" && value.Direction != "download" || value.Bytes < 0 || value.Bytes > BandwidthBytes || value.Duration < 0 || value.Duration > 30 || value.Megabits < 0 || value.Megabits > 1_000_000 {
				return errors.New("node diagnostics: invalid bandwidth measurement")
			}
			targetIndex := slices.IndexFunc(BandwidthTargets, func(target BandwidthTarget) bool {
				return target.Region == value.Region && target.Location == value.Location
			})
			if targetIndex < 0 {
				return errors.New("node diagnostics: invalid bandwidth endpoint")
			}
			seen[key] = true
		}
		return nil
	}
	return errors.New("node diagnostics: invalid kind")
}
