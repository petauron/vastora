package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// BuildOfficialRepository produces immutable objects and the final timestamp
// pointer. It cannot generate/rotate roots or upload content. Publish all other
// files before timestamp.json, under an exclusive channel publication lock.
func BuildOfficialRepository(rootBytes, targetBytes []byte, channel string, previous OfficialAcceptance, now time.Time, signers map[string][]signature.Signer) (map[string][]byte, error) {
	_, acceptance, err := ValidateOfficialTarget(targetBytes, channel, previous, now)
	if err != nil {
		return nil, err
	}
	if acceptance.Revision <= previous.Revision {
		return nil, errors.New("catalog: publication requires a new revision")
	}
	root, err := metadata.Root().FromBytes(rootBytes)
	if err != nil {
		return nil, err
	}
	if err := root.VerifyDelegate("root", root); err != nil {
		return nil, err
	}
	if !root.Signed.ConsistentSnapshot || !root.Signed.Expires.After(now) {
		return nil, errors.New("catalog: publication requires an unexpired consistent-snapshot root")
	}
	// An online publication role must not share a key with the offline root.
	// Otherwise compromise of the routine publisher also permits trust rotation.
	rootKeys := make(map[string]bool)
	for _, key := range root.Signed.Roles["root"].KeyIDs {
		rootKeys[key] = true
	}
	for _, role := range []string{"targets", "snapshot", "timestamp"} {
		entry, ok := root.Signed.Roles[role]
		if !ok {
			return nil, fmt.Errorf("catalog: root is missing %s authorization", role)
		}
		for _, key := range entry.KeyIDs {
			if rootKeys[key] {
				return nil, errors.New("catalog: publication keys must be separate from root keys")
			}
		}
	}
	version := int64(acceptance.Revision)
	files := map[string][]byte{fmt.Sprintf("%d.root.json", root.Signed.Version): rootBytes}
	target, err := metadata.TargetFile().FromBytes(channel+".json", targetBytes, "sha256")
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(targetBytes)
	files["targets/"+hex.EncodeToString(hash[:])+"."+channel+".json"] = targetBytes
	targets := metadata.Targets(acceptance.ExpiresAt)
	targets.Signed.Version = version
	targets.Signed.Targets[channel+".json"] = target
	targetsBytes, err := signOfficialRole(root, "targets", targets, signers["targets"])
	if err != nil {
		return nil, err
	}
	files[fmt.Sprintf("%d.targets.json", version)] = targetsBytes
	snapshot := metadata.Snapshot(acceptance.ExpiresAt)
	snapshot.Signed.Version = version
	snapshot.Signed.Meta["targets.json"] = officialMetaFile(version, targetsBytes)
	snapshotBytes, err := signOfficialRole(root, "snapshot", snapshot, signers["snapshot"])
	if err != nil {
		return nil, err
	}
	files[fmt.Sprintf("%d.snapshot.json", version)] = snapshotBytes
	// Use the explicitly approved publication lifetime for every online role.
	// A hidden shorter timestamp expiry would disable installations before the
	// operator's stated renewal deadline. The client still enforces root expiry.
	timestamp := metadata.Timestamp(acceptance.ExpiresAt)
	timestamp.Signed.Version = version
	timestamp.Signed.Meta["snapshot.json"] = officialMetaFile(version, snapshotBytes)
	files["timestamp.json"], err = signOfficialRole(root, "timestamp", timestamp, signers["timestamp"])
	if err != nil {
		return nil, err
	}
	return files, nil
}

func officialMetaFile(version int64, raw []byte) *metadata.MetaFiles {
	hash := sha256.Sum256(raw)
	return &metadata.MetaFiles{Version: version, Length: int64(len(raw)), Hashes: metadata.Hashes{"sha256": hash[:]}}
}

func signOfficialRole[T metadata.Roles](root *metadata.Metadata[metadata.RootType], role string, value *metadata.Metadata[T], signers []signature.Signer) ([]byte, error) {
	for _, signer := range signers {
		if _, err := value.Sign(signer); err != nil {
			return nil, err
		}
	}
	if err := root.VerifyDelegate(role, value); err != nil {
		return nil, fmt.Errorf("catalog: unauthorized %s signing keys: %w", role, err)
	}
	return json.Marshal(value)
}
