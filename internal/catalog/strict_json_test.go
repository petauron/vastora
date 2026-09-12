package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCatalogRejectsAmbiguousProperties(t *testing.T) {
	raw, err := MarshalCatalog(validCatalog())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, before, after string
	}{
		{"duplicate revision", `"schemaVersion":3`, `"schemaVersion":1,"schemaVersion":3`},
		{"duplicate identical property", `"schemaVersion":3`, `"schemaVersion":3,"schemaVersion":3`},
		{"escaped duplicate property", `"schemaVersion":3`, `"schemaVersion":3,"\u0073chemaVersion":3`},
		{"root case alias", `"schemaVersion":3`, `"SchemaVersion":3`},
		{"app case alias", `"id":"cpa"`, `"ID":"cpa"`},
		{"permission case alias", `"id":"cpa"`, `"id":"cpa","HostAccess":true`},
		{"duplicate permission", `"id":"cpa"`, `"id":"cpa","hostAccess":false,"hostAccess":true`},
		{"localized case alias", `"en":"CPA"`, `"EN":"CPA"`},
		{"duplicate localized property", `"en":"CPA"`, `"en":"Other","en":"CPA"`},
		{"nested case alias", `"secret":true`, `"Secret":true`},
		{"nested duplicate property", `"secret":true`, `"secret":false,"secret":true`},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := strings.Replace(string(raw), test.before, test.after, 1)
			if candidate == string(raw) {
				t.Fatal("test did not change the catalog")
			}
			if _, err := ParseCatalog([]byte(candidate)); err == nil {
				t.Fatal("ambiguous signed catalog accepted")
			}
		})
	}
	// Different JSON spellings of the same property are fine individually.
	// It is their duplication, not escaping or formatting, that is ambiguous.
	escaped := strings.Replace(string(raw), `"schemaVersion":3`, `"\u0073chemaVersion":3`, 1)
	if _, err := ParseCatalog([]byte(escaped)); err != nil {
		t.Fatalf("unambiguous escaped property rejected: %v", err)
	}
}

func TestParseEnvelopeRejectsAmbiguousProperties(t *testing.T) {
	for _, raw := range []string{
		`{"keyId":"first","keyId":"second"}`,
		`{"KeyID":"test"}`,
		`{"keyId":"test","Payload":"data"}`,
		`{"keyId":"test","payload":"one","payload":"two"}`,
		`{"keyId":"test","signature":"one","signature":"two"}`,
	} {
		if _, err := ParseEnvelope([]byte(raw)); err == nil {
			t.Fatalf("ambiguous envelope accepted: %s", raw)
		}
	}
}

func TestStrictJSONChecksRawValuesAndBounds(t *testing.T) {
	for _, raw := range []string{
		`{"raw":{"value":1,"value":2}}`,
		`{"raw":[{"value":1,"value":2}]}`,
		`{"raw":{"value":1,"\u0076alue":2}}`,
		`{"raw":1} {"raw":2}`,
		`{"raw":1,}`,
		`{"raw":[1}`,
		`{"raw":` + strings.Repeat("[", 65) + `0` + strings.Repeat("]", 65) + `}`,
	} {
		var value struct {
			Raw json.RawMessage `json:"raw"`
		}
		if err := decodeStrictJSON([]byte(raw), &value); err == nil {
			t.Fatalf("invalid JSON accepted: %s", raw)
		}
	}
	var value struct {
		Raw json.RawMessage `json:"raw"`
	}
	if err := decodeStrictJSON([]byte(`{"raw":[{"value":1},{"value":2}]}`), &value); err != nil {
		t.Fatalf("same property on separate objects rejected: %v", err)
	}
}

func TestCanonicalAppManifestRejectsNullDefaults(t *testing.T) {
	for _, fieldType := range []string{"string", "boolean", "integer"} {
		t.Run(fieldType, func(t *testing.T) {
			app := validCatalog().Apps[0]
			raw := json.RawMessage(" \nnull\t")
			app.Config[0].Secret = false
			app.Config[0].Type = fieldType
			app.Config[0].Default = &raw
			if _, err := CanonicalAppManifest(app); err == nil {
				t.Fatal("null default coerced into a valid zero value")
			}
		})
	}
}
