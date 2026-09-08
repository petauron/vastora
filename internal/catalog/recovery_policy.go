package catalog

// RecoveryPolicy is the catalog's recovery contract, versioned independently
// from executable package manifests: adding backup documentation must not
// force a running application upgrade. Unknown packages remain unsupported.
type RecoveryPolicy struct {
	Version     int      `json:"version"`
	Consistency string   `json:"consistency"`
	Volumes     []string `json:"volumes"`
}

func OfficialRecoveryPolicy(appKey, version string) (RecoveryPolicy, bool) {
	policy := RecoveryPolicy{Version: 1, Consistency: "stopped"}
	switch {
	case appKey == "vastora-official/3x-ui" && version == "3.7.0":
		policy.Volumes = []string{"vastora-3x-ui-db", "vastora-3x-ui-cert", "vastora-3x-ui-acme"}
	case appKey == "vastora-official/cpa" && version == "7.2.130":
		policy.Volumes = []string{"vastora-cpa-auths", "vastora-cpa-logs", "vastora-cpa-plugins"}
	case appKey == "vastora-official/keeper" && version == "1.14.1":
		policy.Volumes = []string{"vastora-cpa-keeper-data"}
	case appKey == "vastora-official/komari-agent" && version == "1.2.60":
		policy.Consistency, policy.Volumes = "reconstructible", []string{}
	default:
		return RecoveryPolicy{}, false
	}
	return policy, true
}
