package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const OfficialSourceIdentity = "vastora-official"

// Distribution location is not a trust anchor: independent TUF verification
// remains mandatory even when using the project's default download origin.
const OfficialOrigin = "https://downloads.petauron.com/vastora/catalog/"

// OfficialTarget is a TUF target, not a self-authenticating signature envelope.
// Callers must verify its exact bytes against trusted TUF metadata first.
type OfficialTarget struct {
	Source      string          `json:"source"`
	Channel     string          `json:"channel"`
	Revision    uint64          `json:"revision"`
	GeneratedAt time.Time       `json:"generatedAt"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	Catalog     json.RawMessage `json:"catalog"`
}

// OfficialAcceptance must live independently of the editable source record.
// A successful validation is only a proposal: persist it atomically with the
// verified catalog and TUF metadata, never before those writes succeed.
type OfficialAcceptance struct {
	Channel    string    `json:"channel"`
	Revision   uint64    `json:"revision"`
	SHA256     string    `json:"sha256"`
	ObservedAt time.Time `json:"observedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func ValidateOfficialTarget(raw []byte, channel string, previous OfficialAcceptance, now time.Time) (Catalog, OfficialAcceptance, error) {
	reject := func(message string) (Catalog, OfficialAcceptance, error) {
		return Catalog{}, OfficialAcceptance{}, errors.New("catalog: " + message)
	}
	if len(raw) == 0 || int64(len(raw)) > MaxEnvelopeBytes {
		return reject("official target exceeds size limits")
	}
	if channel == "" || now.IsZero() || now.Before(previous.ObservedAt) {
		return reject("official target channel or local clock is invalid")
	}
	var target OfficialTarget
	if err := decodeStrictJSON(raw, &target); err != nil {
		return reject("invalid official target")
	}
	if target.Source != OfficialSourceIdentity || target.Channel != channel || (previous.Channel != "" && previous.Channel != channel) {
		return reject("official target identity or channel mismatch")
	}
	if target.Revision == 0 || target.Revision > uint64(maxPortableInteger) || target.Revision < previous.Revision {
		return reject("official target revision rollback")
	}
	if target.GeneratedAt.IsZero() || target.GeneratedAt.After(now) || !target.ExpiresAt.After(target.GeneratedAt) || !target.ExpiresAt.After(now) {
		return reject("official target is expired or has invalid timestamps")
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	if target.Revision == previous.Revision && digest != previous.SHA256 {
		return reject("official target revision content changed")
	}
	value, err := ParseCatalog(target.Catalog)
	if err != nil {
		return Catalog{}, OfficialAcceptance{}, err
	}
	return value, OfficialAcceptance{Channel: channel, Revision: target.Revision, SHA256: digest, ObservedAt: now.UTC(), ExpiresAt: target.ExpiresAt.UTC()}, nil
}
