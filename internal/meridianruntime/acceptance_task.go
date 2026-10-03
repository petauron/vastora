package meridianruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"

	"github.com/petauron/vastora/internal/landing"
)

const AcceptanceKind = "meridian.recovery.acceptance"

// AcceptanceTask is secret execution material, never command metadata. Each
// command verifies one original credential; Center must require every intended
// credential before releasing the recovery fence.
type AcceptanceTask struct {
	VerifierFingerprint string           `json:"verifierFingerprint"`
	OperationID         string           `json:"operationId"`
	PlanRevision        string           `json:"planRevision"`
	TargetAgentID       string           `json:"targetAgentId"`
	VerifierAgentID     string           `json:"verifierAgentId"`
	IdentityFingerprint string           `json:"identityFingerprint"`
	Client              AcceptanceClient `json:"client"`
	ExpectedExit        string           `json:"expectedExit"`
}

func (t AcceptanceTask) Validate() error {
	ip, err := netip.ParseAddr(t.ExpectedExit)
	if t.VerifierFingerprint == "" || t.OperationID == "" || t.PlanRevision == "" || t.IdentityFingerprint == "" || t.TargetAgentID == "" || t.VerifierAgentID == "" || t.TargetAgentID == t.VerifierAgentID || err != nil || !landing.PublicIP(ip) {
		return errors.New("meridian: invalid recovery acceptance task")
	}
	_, err = t.Client.Config(1080)
	return err
}

// Digest binds the result to all secret and public inputs. Possession of this
// digest alone is not evidence: Center accepts it only in an authenticated,
// successfully projected execution and rechecks current recovery authority.
func (t AcceptanceTask) Digest() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type AcceptanceResult struct {
	TaskDigest string `json:"taskDigest"`
}

func (r AcceptanceResult) Validate(t AcceptanceTask) error {
	digest, err := t.Digest()
	if err != nil || r.TaskDigest != digest {
		return errors.New("meridian: mismatched recovery acceptance result")
	}
	return nil
}
