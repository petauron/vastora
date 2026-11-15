package osreinstall

import (
	"errors"
	"reflect"
	"slices"
)

// RecheckObservedHost compares the last reviewed inspection with fresh evidence
// immediately before preparing or arming an installer. It does not clear the
// network, recovery or workload review requirements by itself.
func RecheckObservedHost(reviewed, current Inspection) error {
	if reviewed.Validate() != nil || current.Validate() != nil {
		return errors.New("reinstall: invalid host evidence")
	}
	if reviewed.BootID == "" || reviewed.BootID != current.BootID || reviewed.OSID == "" || reviewed.OSID != current.OSID || reviewed.OSVersion == "" || reviewed.OSVersion != current.OSVersion || reviewed.Virtualization != current.Virtualization || reviewed.Architecture != current.Architecture || reviewed.BootMode == "" || reviewed.BootMode != current.BootMode || reviewed.Hostname == "" || reviewed.Hostname != current.Hostname || reviewed.SSHHostKeySHA256 == "" || reviewed.SSHHostKeySHA256 != current.SSHHostKeySHA256 {
		return errors.New("reinstall: host identity changed since review")
	}
	if reviewed.Disk != current.Disk || !partitionIDPattern.MatchString(reviewed.Disk.PartitionTableID) {
		return errors.New("reinstall: system disk changed since review")
	}
	a, b := reviewed.Network, current.Network
	if !reflect.DeepEqual(a.Additional, b.Additional) || a.Interface != b.Interface || a.MAC != b.MAC || !slices.Equal(a.Addresses, b.Addresses) || !slices.Equal(a.Gateways, b.Gateways) || !slices.Equal(a.DNS, b.DNS) || reviewed.NetworkCandidate != current.NetworkCandidate || !slices.Equal(reviewed.Limitations, current.Limitations) || !slices.Equal(reviewed.MissingPrerequisites, current.MissingPrerequisites) {
		return errors.New("reinstall: network evidence changed since review")
	}
	return nil
}
