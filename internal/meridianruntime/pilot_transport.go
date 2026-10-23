package meridianruntime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/petauron/glivela"
)

// The authenticated controller retains its original accounting policy. Only
// private peer transport material and revision may differ, never identity,
// address, DNS, exit, credentials, authorization or network policy.
func transportOriginDigest(node quotaNode) (string, error) {
	invalid := errors.New("meridianruntime: transport accounting origin differs")
	q := node.Quota
	if q == nil || len(q.TransportOrigin) == 0 {
		return "", invalid
	}
	var origin, current quotaDesired
	if json.Unmarshal(q.TransportOrigin, &origin) != nil || json.Unmarshal(q.Base, &current) != nil || origin.Validate() != nil || current.Validate() != nil || origin.Revision > current.Revision {
		return "", invalid
	}
	left, err := canonicalQuotaJSON(origin.Network)
	right, other := canonicalQuotaJSON(current.Network)
	if err != nil || other != nil || !bytes.Equal(left, right) || len(origin.Landing) != 0 && string(origin.Landing) != "null" {
		return "", invalid
	}
	decode := func(raw []byte) (map[string]any, error) {
		var value map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&value)
		return value, err
	}
	a, err := decode(origin.Entry)
	b, other := decode(current.Entry)
	if err != nil || other != nil || a == nil || b == nil {
		return "", invalid
	}
	oldPeers, oldOK := a["peers"].([]any)
	newPeers, newOK := b["peers"].([]any)
	if !oldOK || !newOK || len(oldPeers) != len(newPeers) {
		return "", invalid
	}
	for i := range oldPeers {
		old, ok := oldPeers[i].(map[string]any)
		next, nextOK := newPeers[i].(map[string]any)
		if !ok || !nextOK || !validPrivateTransport(old["transport"]) || !validPrivateTransport(next["transport"]) {
			return "", invalid
		}
		delete(old, "transport")
		delete(next, "transport")
	}
	delete(a, "revision")
	delete(b, "revision")
	if !reflect.DeepEqual(a, b) {
		return "", invalid
	}
	return quotaBaseDigest(q.TransportOrigin)
}

func validPrivateTransport(value any) bool {
	if value == nil {
		return true
	}
	t, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if len(t) == 0 {
		return true
	}
	if len(t) != 2 || t["protocol"] != "ss2022" {
		return false
	}
	key, ok := t["key"].(string)
	decoded, err := base64.StdEncoding.Strict().DecodeString(key)
	defer clear(decoded)
	return ok && err == nil && len(decoded) == 16 && base64.StdEncoding.EncodeToString(decoded) == key
}

// Unlimited legacy checkpoints remain unlimited. Once transport switching
// begins, consume the retained cumulative projection rather than a restarted
// worker's raw counters. No new ledger or database checkpoint identity is made.
func ReadUnlimitedPilotReceipt(path string, previous PilotCounters, now time.Time) (PilotReceipt, error) {
	invalid := errors.New("meridianruntime: unlimited transport receipt refused")
	if previous.Quota != nil {
		return PilotReceipt{}, invalid
	}
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return PilotReceipt{}, err
	}
	defer clear(raw)
	var nodes []quotaNode
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return PilotReceipt{}, invalid
	}
	hasQuota := false
	for _, n := range nodes {
		if n.ID == previous.EntryID || n.CertificateID == previous.CertificateID {
			hasQuota = hasQuota || n.Quota != nil
		}
	}
	if !hasQuota {
		return readPilotReceiptBytes(raw, previous.EntryID, previous.CertificateID, previous.ConfigSHA256, previous.Revision, now)
	}
	node, err := readQuotaNode(raw, previous.EntryID, previous.CertificateID)
	if err != nil {
		return PilotReceipt{}, err
	}
	q := node.Quota
	if _, err := transportOriginDigest(node); err != nil || q.TotalBytes != 0 || q.ResetDay != 0 || q.Timezone != "" {
		return PilotReceipt{}, invalid
	}
	var origin quotaDesired
	if json.Unmarshal(q.TransportOrigin, &origin) != nil || origin.Revision != previous.Revision || origin.ConfigSHA256 != previous.ConfigSHA256 {
		return PilotReceipt{}, invalid
	}
	r, err := readPilotReceiptBytes(raw, previous.EntryID, previous.CertificateID, node.Desired.ConfigSHA256, node.Desired.Revision, now)
	if err != nil {
		return PilotReceipt{}, err
	}
	f := q.Forwarded
	if q.Raw == nil || !f.SampledAt.Equal(q.Raw.SampledAt) || !f.SampledAt.Equal(r.SampledAt) || f.SampledAt.After(now) || now.Sub(f.SampledAt) > 30*time.Second {
		return PilotReceipt{}, invalid
	}
	r.ConfigSHA256, r.StartedAt = previous.ConfigSHA256, f.StartedAt
	r.Counters = nil
	for id, c := range f.Counters {
		r.Counters = append(r.Counters, glivela.CounterSnapshot{CredentialID: id, UpBytes: c.Upload, DownBytes: c.Download})
	}
	slices.SortFunc(r.Counters, func(a, b glivela.CounterSnapshot) int { return strings.Compare(a.CredentialID, b.CredentialID) })
	return r, nil
}
