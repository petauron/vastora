package osreinstall

import "errors"

const TaskKind = "node.os-reinstall"

// Task is delivered inside the existing one-use, Agent-encrypted execution
// envelope. RecoveryPassword is never exposed to the web or task event log.
type Task struct {
	Inspection       Inspection `json:"inspection"`
	Username         string     `json:"username"`
	SSHPort          int        `json:"sshPort"`
	IdentitySHA256   string     `json:"identitySha256"`
	RecoveryPassword string     `json:"recoveryPassword"`
}

func (t Task) Validate() error {
	if t.Inspection.Validate() != nil || len(t.Inspection.MissingPrerequisites) != 0 || t.Inspection.Architecture != "amd64" ||
		t.Inspection.BootMode != "bios" || t.Inspection.BootID == "" ||
		t.Inspection.Hostname == "" || t.Inspection.SSHHostKeySHA256 == "" ||
		t.Inspection.OSID == "" || t.Inspection.OSVersion == "" ||
		!partitionIDPattern.MatchString(t.Inspection.Disk.PartitionTableID) ||
		(t.Inspection.NetworkCandidate != "netplan_static_ipv4" && t.Inspection.NetworkCandidate != "ifupdown_static_ipv4") ||
		!safeUser.MatchString(t.Username) || t.SSHPort < 1 || t.SSHPort > 65535 ||
		!safeAgentHash.MatchString(t.IdentitySHA256) || len(t.RecoveryPassword) < 32 || len(t.RecoveryPassword) > 256 {
		return errors.New("reinstall: incomplete or unsupported task")
	}
	for _, limitation := range t.Inspection.Limitations {
		if limitation != "network_configuration_requires_review" {
			return errors.New("reinstall: host requires additional network review")
		}
	}
	return nil
}

// BackupEvidence is sent over the authenticated Agent channel. Ciphertext is
// already encrypted with the per-operation password from the task envelope.
// The network plan is sealed again under Center's key before persistence.
type BackupEvidence struct {
	Ciphertext       []byte            `json:"ciphertext"`
	AccessCiphertext []byte            `json:"accessCiphertext"`
	Digest           string            `json:"digest"`
	Network          StaticNetworkPlan `json:"network"`
}

const MaxBackupBytes = 600 << 10

// ArmEvidence binds the one-time boot grant to the saved backup and to the
// installer staged by the Agent. A lost grant response is not retry authority.
type ArmEvidence struct {
	BackupDigest   string `json:"backupDigest"`
	PreparedDigest string `json:"preparedDigest"`
}

// ReconnectReport is observed by the restored Agent after a new boot. Center
// compares it with the authenticated identity and its own pre-install record.
type ReconnectReport struct {
	TaskID  string            `json:"taskId"`
	After   Inspection        `json:"after"`
	Network StaticNetworkPlan `json:"network"`
}

// ReconnectMarker contains no credential. It is written by the installer into
// the restored root-only Agent directory for one authenticated post-boot report.
type ReconnectMarker struct {
	TaskID      string `json:"taskId"`
	ExecutionID string `json:"executionId"`
}

func (m ReconnectMarker) Validate() error {
	if !safeAgentID.MatchString(m.TaskID) || !safeAgentID.MatchString(m.ExecutionID) {
		return errors.New("reinstall: invalid reconnect marker")
	}
	return nil
}

// PreparedStage names the private, digest-bound installer material.
type PreparedStage struct {
	Directory string
	Digest    string
}

// StageReview is non-secret, digest-bound material used by the host helper to
// recheck the exact host immediately before modifying its one-time boot entry.
type StageReview struct {
	Spec             InstallerSpec `json:"spec"`
	Inspection       Inspection    `json:"inspection"`
	ExecutableDigest string        `json:"executableDigest"`
}
