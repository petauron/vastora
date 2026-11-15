package osreinstall

import (
	"errors"
	"slices"
)

// ReconnectEvidence is supplied from an authenticated report of the original
// Agent. A heartbeat alone does not prove which system booted or that the
// network was restored. This check does not initiate or authorize an install.
type ReconnectEvidence struct {
	ExpectedAgentID      string
	ReportedAgentID      string
	ExpectedIdentityHash string
	ReportedIdentityHash string
	Before               Inspection
	After                Inspection
	BeforeNetwork        StaticNetworkPlan
	AfterNetwork         StaticNetworkPlan
}

// VerifyReconnect checks the reviewed physical-network policies. The caller
// must bind the reported identity to an authenticated Agent session and retain
// the reviewed pre-install snapshot outside the target machine.
func VerifyReconnect(e ReconnectEvidence) error {
	bad := func() error { return errors.New("reinstall: reconnect evidence is incomplete or does not match") }
	if !safeAgentID.MatchString(e.ExpectedAgentID) || e.ExpectedAgentID != e.ReportedAgentID ||
		!safeAgentHash.MatchString(e.ExpectedIdentityHash) || e.ExpectedIdentityHash != e.ReportedIdentityHash ||
		e.Before.Validate() != nil || e.After.Validate() != nil {
		return bad()
	}
	if e.Before.BootID == "" || e.After.BootID == "" || e.Before.BootID == e.After.BootID ||
		e.After.OSID != "debian" || e.After.OSVersion != "13" ||
		e.Before.Virtualization != e.After.Virtualization || e.Before.Architecture != e.After.Architecture ||
		e.Before.BootMode == "" || e.Before.BootMode != e.After.BootMode ||
		e.Before.Hostname == "" || e.Before.Hostname != e.After.Hostname ||
		e.Before.SSHHostKeySHA256 == "" || e.Before.SSHHostKeySHA256 != e.After.SSHHostKeySHA256 ||
		e.Before.Disk.Path != e.After.Disk.Path || e.Before.Disk.MajorMinor != e.After.Disk.MajorMinor ||
		e.Before.Disk.Bytes != e.After.Disk.Bytes {
		return bad()
	}
	a, b := e.BeforeNetwork, e.AfterNetwork
	if _, _, _, err := a.RenderStaticNetwork(); err != nil {
		return bad()
	}
	if _, _, _, err := b.RenderStaticNetwork(); err != nil {
		return bad()
	}
	if !sameNetworkPlan(a, b) || matchPlanObservation(a, e.Before.Network) != nil || matchPlanObservation(b, e.After.Network) != nil || !a.DynamicDNS && !slices.Equal(e.Before.Network.DNS, e.After.Network.DNS) {
		return bad()
	}
	for _, state := range []Inspection{e.Before, e.After} {
		for _, limitation := range state.Limitations {
			if limitation != "network_configuration_requires_review" {
				return bad()
			}
		}
	}
	return nil
}
