package tailscalehost

// UpdateStatus is a node observation, not an authorization to change a host.
// LatestVersion is the upstream stable release; a node's package source must
// still be checked before an update can be offered or performed.
type UpdateStatus struct {
	InstalledVersion string `json:"installedVersion,omitempty"`
	RunningVersion   string `json:"runningVersion,omitempty"`
	LatestVersion    string `json:"latestVersion,omitempty"`
	CheckedAt        string `json:"checkedAt,omitempty"`
	Error            string `json:"error,omitempty"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	UpgradeReady     bool   `json:"upgradeReady"`
}
