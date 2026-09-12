package catalog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOfficialTargetAcceptance(t *testing.T) {
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	target := OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: 2, GeneratedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Catalog: payload}
	encode := func(value OfficialTarget) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := encode(target)
	_, accepted, err := ValidateOfficialTarget(raw, "stable", OfficialAcceptance{}, now)
	if err != nil || accepted.Revision != 2 || accepted.SHA256 == "" {
		t.Fatalf("acceptance=%+v err=%v", accepted, err)
	}
	if _, _, err := ValidateOfficialTarget(raw, "stable", accepted, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*OfficialTarget)
	}{
		{"foreign identity", func(v *OfficialTarget) { v.Source = "third-party" }},
		{"foreign channel", func(v *OfficialTarget) { v.Channel = "preview" }},
		{"rollback", func(v *OfficialTarget) { v.Revision = 1 }},
		{"zero revision", func(v *OfficialTarget) { v.Revision = 0 }},
		{"same revision different bytes", func(v *OfficialTarget) { v.GeneratedAt = v.GeneratedAt.Add(time.Minute) }},
		{"expired", func(v *OfficialTarget) { v.Revision = 3; v.ExpiresAt = now }},
		{"future", func(v *OfficialTarget) { v.Revision = 3; v.GeneratedAt = now.Add(time.Minute) }},
		{"invalid catalog", func(v *OfficialTarget) { v.Revision = 3; v.Catalog = json.RawMessage(`{}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := target
			test.change(&changed)
			if _, next, err := ValidateOfficialTarget(encode(changed), "stable", accepted, now); err == nil || next.Revision != 0 {
				t.Fatalf("invalid target accepted: %+v %v", next, err)
			}
		})
	}
	if _, _, err := ValidateOfficialTarget(raw, "stable", accepted, now.Add(-time.Second)); err == nil {
		t.Fatal("clock rollback accepted")
	}
	// A repeated/304-equivalent cached target must not renew signed expiry.
	if _, _, err := ValidateOfficialTarget(raw, "stable", accepted, target.ExpiresAt); err == nil {
		t.Fatal("cached expiry extended")
	}
	if _, _, err := ValidateOfficialTarget(append(raw, []byte(` {}`)...), "stable", accepted, now); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	target.Revision++
	if _, next, err := ValidateOfficialTarget(encode(target), "stable", accepted, now); err != nil || next.Revision != 3 {
		t.Fatalf("valid revision rejected: %+v %v", next, err)
	}
}

func TestOfficialTargetRejectsAmbiguousProperties(t *testing.T) {
	payload, err := MarshalCatalog(validCatalog())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: 2, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, before, after string
	}{
		{"duplicate source", `"source":"vastora-official"`, `"source":"private","source":"vastora-official"`},
		{"source case alias", `"source":`, `"Source":`},
		{"revision case alias", `"revision":2`, `"Revision":2`},
		{"duplicate revision", `"revision":2`, `"revision":1,"revision":2`},
		{"duplicate escaped revision", `"revision":2`, `"revision":1,"\u0072evision":2`},
		{"duplicate channel", `"channel":"stable"`, `"channel":"preview","channel":"stable"`},
		{"expiry case alias", `"expiresAt":`, `"ExpiresAt":`},
		{"nested duplicate", `"schemaVersion":3`, `"schemaVersion":2,"schemaVersion":3`},
		{"nested case alias", `"schemaVersion":3`, `"SchemaVersion":3`},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := strings.Replace(string(raw), test.before, test.after, 1)
			if candidate == string(raw) {
				t.Fatal("test did not change the target")
			}
			if _, accepted, err := ValidateOfficialTarget([]byte(candidate), "stable", OfficialAcceptance{}, now); err == nil || accepted.Revision != 0 {
				t.Fatalf("ambiguous target accepted: %+v err=%v", accepted, err)
			}
		})
	}
}
