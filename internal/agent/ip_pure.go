package agent

import (
	"encoding/json"
	"math"
	"net"
	"time"

	"github.com/petauron/vastora/internal/ipquality"
)

func missingIPPure(address string, now time.Time) ipquality.IPPureResult {
	status := "unavailable"
	if net.ParseIP(address).To4() == nil {
		// IPPure does not calculate IPv6 risk. Never use a different IPv4 exit.
		status = "unsupported"
	}
	return ipquality.IPPureResult{Provider: ipquality.IPPureProvider, Status: status, CheckedAt: now.UTC().Format(time.RFC3339Nano)}
}

func parseIPPure(data []byte, address string, now time.Time) ipquality.IPPureResult {
	result := ipquality.IPPureResult{Provider: ipquality.IPPureProvider, Status: "invalid_response", CheckedAt: now.UTC().Format(time.RFC3339Nano)}
	var raw struct {
		IP          string   `json:"ip"`
		FraudScore  *float64 `json:"fraudScore"`
		Residential *bool    `json:"isResidential"`
		Broadcast   *bool    `json:"isBroadcast"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.FraudScore == nil || math.IsNaN(*raw.FraudScore) || math.IsInf(*raw.FraudScore, 0) || *raw.FraudScore < 0 || *raw.FraudScore > 100 || net.ParseIP(raw.IP) == nil {
		return result
	}
	if !net.ParseIP(raw.IP).Equal(net.ParseIP(address)) {
		result.Status = "ip_mismatch"
		result.Address = raw.IP
		return result
	}
	if net.ParseIP(raw.IP).To4() == nil {
		result.Status = "unsupported"
		return result
	}
	result.Status, result.Address, result.RiskScore = "ok", raw.IP, raw.FraudScore
	result.Residential, result.Broadcast = raw.Residential, raw.Broadcast
	return result
}
