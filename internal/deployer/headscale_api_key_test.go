package deployer

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseHeadscaleAPIKeyExpiration(t *testing.T) {
	want := time.Date(2027, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	tests := map[string]json.RawMessage{
		"protobuf timestamp": json.RawMessage(`{"seconds":1798859045,"nanos":123456789}`),
		"RFC3339 string":     json.RawMessage(`"2027-01-02T03:04:05.123456789Z"`),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseHeadscaleAPIKeyExpiration(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Fatalf("expiration = %s, want %s", got, want)
			}
		})
	}
}

func TestParseHeadscaleAPIKeyExpirationRejectsMalformedTimestamp(t *testing.T) {
	for _, encoded := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`{"seconds":"nope","nanos":0}`),
		json.RawMessage(`{"seconds":1798859045,"nanos":1000000000}`),
	} {
		if _, err := parseHeadscaleAPIKeyExpiration(encoded); err == nil {
			t.Fatalf("expected %s to be rejected", encoded)
		}
	}
}

func TestFindHeadscaleAPIKeyRecordNormalizesAuthoritativePrefix(t *testing.T) {
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Nanosecond)
	records := []headscaleAPIKeyRecord{{
		ID:         json.RawMessage(`1`),
		Prefix:     "hskey-api-qmjwbNmFsG_f-***",
		Expiration: json.RawMessage(`"` + expiresAt.Format(time.RFC3339Nano) + `"`),
	}}

	rotation, found, err := findHeadscaleAPIKeyRecord(records, "qmjwbNmFsG_f")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected the masked authoritative prefix to match the canonical key prefix")
	}
	if rotation.APIKeyID != 1 || rotation.APIKeyPrefix != "qmjwbNmFsG_f" || !rotation.APIKeyExpiresAt.Equal(expiresAt) {
		t.Fatalf("rotation = %#v", rotation)
	}
}
