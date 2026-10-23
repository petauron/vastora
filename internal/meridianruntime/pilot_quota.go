package meridianruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/glivela"
)

// PilotQuota pins only the staged entry's quota projection. Existing account
// and lifetime ledger ownership is unchanged. BaseSHA256 binds the complete
// retained desired document, including network policy and credential identity.
type PilotQuota struct {
	TotalBytes  int64     `json:"totalBytes,string"`
	UsedBytes   int64     `json:"usedBytes,string"`
	ResetDay    int       `json:"resetDay"`
	Timezone    string    `json:"timezone"`
	LastResetAt time.Time `json:"lastResetAt,omitempty"`
	NextResetAt time.Time `json:"nextResetAt"`
	CutoverAt   time.Time `json:"cutoverAt"`
	BaseSHA256  string    `json:"baseSha256"`
}

func validPilotQuotaCycle(q PilotQuota, now time.Time) bool {
	zone, err := time.LoadLocation(q.Timezone)
	if err != nil || q.ResetDay < 1 || q.ResetDay > 31 {
		return false
	}
	boundary := func(year int, month time.Month) time.Time {
		first := time.Date(year, month, 1, 0, 0, 0, 0, zone)
		day := min(q.ResetDay, first.AddDate(0, 1, -1).Day())
		return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, zone)
	}
	last := q.LastResetAt.In(zone)
	return q.LastResetAt.Equal(boundary(last.Year(), last.Month())) && q.NextResetAt.Equal(boundary(last.Year(), last.Month()+1)) && !q.LastResetAt.After(now) && q.NextResetAt.After(now)
}

type quotaDesired struct {
	glivela.DesiredArtifact
	Entry   json.RawMessage `json:"entry"`
	Network json.RawMessage `json:"entryNetwork"`
	Landing json.RawMessage `json:"landing"`
}

type quotaTraffic struct {
	StartedAt time.Time `json:"startedAt"`
	SampledAt time.Time `json:"sampledAt"`
	Counters  map[string]struct {
		Upload   int64 `json:"upload,string"`
		Download int64 `json:"download,string"`
	} `json:"counters"`
}

type quotaNode struct {
	ID            string       `json:"id"`
	CertificateID string       `json:"certificateId"`
	Desired       quotaDesired `json:"desired"`
	Quota         *struct {
		PilotQuota
		Base            json.RawMessage `json:"base"`
		TransportOrigin json.RawMessage `json:"transportOrigin,omitempty"`
		Raw             *quotaTraffic   `json:"raw"`
		Forwarded       *quotaTraffic   `json:"forwarded"`
	} `json:"entryQuota"`
}

func canonicalQuotaJSON(raw []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func quotaBaseDigest(raw []byte) (string, error) {
	canonical, err := canonicalQuotaJSON(raw)
	if err != nil {
		return "", err
	}
	defer clear(canonical)
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func readQuotaNode(raw []byte, entry, certificate string) (quotaNode, error) {
	invalid := errors.New("meridianruntime: staged quota source differs")
	var nodes []quotaNode
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return quotaNode{}, invalid
	}
	var found *quotaNode
	for i := range nodes {
		n := &nodes[i]
		if n.ID != entry && n.CertificateID != certificate {
			continue
		}
		if found != nil || n.ID != entry || n.CertificateID != certificate || n.Quota == nil {
			return quotaNode{}, invalid
		}
		found = n
	}
	if found == nil {
		return quotaNode{}, invalid
	}
	q := found.Quota
	if q.TotalBytes < 0 || q.UsedBytes < 0 || q.ResetDay < 0 || q.ResetDay > 31 || q.CutoverAt.IsZero() || q.Forwarded == nil || !q.Forwarded.StartedAt.Equal(q.CutoverAt) {
		return quotaNode{}, invalid
	}
	if q.ResetDay == 0 {
		if !q.NextResetAt.IsZero() || !q.LastResetAt.IsZero() {
			return quotaNode{}, invalid
		}
	} else if q.Timezone == "" || q.NextResetAt.IsZero() || !q.LastResetAt.IsZero() && !q.NextResetAt.After(q.LastResetAt) {
		return quotaNode{}, invalid
	} else if _, err := time.LoadLocation(q.Timezone); err != nil {
		return quotaNode{}, invalid
	}
	var base quotaDesired
	if json.Unmarshal(q.Base, &base) != nil || base.Validate() != nil || found.Desired.Validate() != nil || found.Desired.Revision < base.Revision {
		return quotaNode{}, invalid
	}
	if len(base.Landing) != 0 && !bytes.Equal(base.Landing, []byte("null")) || len(found.Desired.Landing) != 0 && !bytes.Equal(found.Desired.Landing, []byte("null")) {
		return quotaNode{}, invalid
	}
	// The controller may only toggle credential authorization and increment
	// revision. No key, route, listener, SNI or transport-policy edits are accepted.
	var expected map[string]any
	d := json.NewDecoder(bytes.NewReader(base.Entry))
	d.UseNumber()
	if d.Decode(&expected) != nil || expected == nil {
		return quotaNode{}, invalid
	}
	expected["revision"] = json.Number(strconv.FormatUint(found.Desired.Revision, 10))
	if q.TotalBytes > 0 && q.UsedBytes >= q.TotalBytes {
		credentials, ok := expected["credentials"].([]any)
		if !ok {
			return quotaNode{}, invalid
		}
		for _, item := range credentials {
			material, ok := item.(map[string]any)
			if !ok {
				return quotaNode{}, invalid
			}
			credential, ok := material["credential"].(map[string]any)
			if !ok {
				return quotaNode{}, invalid
			}
			credential["enabled"] = false
		}
		if grants, ok := expected["grants"].([]any); ok {
			for _, item := range grants {
				grant, ok := item.(map[string]any)
				if !ok {
					return quotaNode{}, invalid
				}
				for _, key := range []string{"base", "route"} {
					credential, ok := grant[key].(map[string]any)
					if !ok {
						return quotaNode{}, invalid
					}
					credential["enabled"] = false
				}
			}
		}
	}
	want, err := json.Marshal(expected)
	if err != nil {
		return quotaNode{}, invalid
	}
	actual, err := canonicalQuotaJSON(found.Desired.Entry)
	if err != nil || !bytes.Equal(want, actual) {
		return quotaNode{}, invalid
	}
	network, err := canonicalQuotaJSON(base.Network)
	if err != nil {
		return quotaNode{}, invalid
	}
	actualNetwork, err := canonicalQuotaJSON(found.Desired.Network)
	if err != nil || !bytes.Equal(network, actualNetwork) {
		return quotaNode{}, invalid
	}
	return *found, nil
}

// ReadPilotQuotaSeed is used only in the activation transaction, after the
// existing complete-plan check and final old-core counter collection.
func ReadPilotQuotaSeed(path, entry, certificate, digest string, revision uint64, expected PilotQuota) (PilotQuota, error) {
	invalid := errors.New("meridianruntime: quota seed differs from final legacy policy")
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return PilotQuota{}, err
	}
	defer clear(raw)
	node, err := readQuotaNode(raw, entry, certificate)
	if err != nil {
		return PilotQuota{}, err
	}
	q := node.Quota
	actual := q.PilotQuota
	actual.BaseSHA256 = ""
	expected.BaseSHA256 = ""
	if actual.TotalBytes != expected.TotalBytes || actual.UsedBytes != expected.UsedBytes || actual.ResetDay != expected.ResetDay || actual.Timezone != expected.Timezone || !actual.CutoverAt.Equal(expected.CutoverAt) || !actual.LastResetAt.Equal(expected.LastResetAt) || !actual.NextResetAt.Equal(expected.NextResetAt) || q.Raw != nil || !q.Forwarded.SampledAt.Equal(expected.CutoverAt) || node.Desired.ConfigSHA256 != digest || node.Desired.Revision != revision {
		return PilotQuota{}, invalid
	}
	for _, counter := range q.Forwarded.Counters {
		if counter.Upload != 0 || counter.Download != 0 {
			return PilotQuota{}, invalid
		}
	}
	actual.BaseSHA256, err = quotaBaseDigest(q.Base)
	return actual, err
}

func ReadQuotaPilotReceipt(path string, previous PilotCounters, now time.Time) (PilotReceipt, error) {
	invalid := errors.New("meridianruntime: staged quota receipt refused")
	if previous.Quota == nil {
		return PilotReceipt{}, invalid
	}
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return PilotReceipt{}, err
	}
	defer clear(raw)
	node, err := readQuotaNode(raw, previous.EntryID, previous.CertificateID)
	if err != nil {
		return PilotReceipt{}, err
	}
	q, expected := node.Quota, previous.Quota
	base, err := quotaBaseDigest(q.Base)
	if len(q.TransportOrigin) != 0 {
		base, err = transportOriginDigest(node)
	}
	if err != nil || base != expected.BaseSHA256 || q.TotalBytes != expected.TotalBytes || q.ResetDay != expected.ResetDay || q.Timezone != expected.Timezone || !q.CutoverAt.Equal(expected.CutoverAt) || !q.CutoverAt.Equal(previous.CutoverAt) {
		return PilotReceipt{}, invalid
	}
	receipt, err := readPilotReceiptBytes(raw, previous.EntryID, previous.CertificateID, node.Desired.ConfigSHA256, node.Desired.Revision, now)
	if err != nil {
		return PilotReceipt{}, err
	}
	f := q.Forwarded
	if f.SampledAt.After(now) || now.Sub(f.SampledAt) > 30*time.Second || q.Raw == nil || !f.SampledAt.Equal(q.Raw.SampledAt) || !f.SampledAt.Equal(receipt.SampledAt) {
		return PilotReceipt{}, invalid
	}
	// Pin accounting to its original stream identity; runtime health above was
	// separately checked against the currently applied quota revision.
	receipt.ConfigSHA256, receipt.StartedAt, receipt.SampledAt = previous.ConfigSHA256, f.StartedAt, f.SampledAt
	receipt.Counters = nil
	for id, c := range f.Counters {
		receipt.Counters = append(receipt.Counters, glivela.CounterSnapshot{CredentialID: id, UpBytes: c.Upload, DownBytes: c.Download})
	}
	slices.SortFunc(receipt.Counters, func(a, b glivela.CounterSnapshot) int { return strings.Compare(a.CredentialID, b.CredentialID) })
	projection := q.PilotQuota
	projection.BaseSHA256 = base
	receipt.Quota = &projection
	return receipt, nil
}
