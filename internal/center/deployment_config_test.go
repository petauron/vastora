package center

import (
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
)

func TestNormalizeDeploymentConfigCanonicalizesPortableIntegerDefaults(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`1.0`, `1e0`} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			defaultValue := json.RawMessage(value)
			manifest := catalog.AppManifest{Config: []catalog.ConfigField{{
				Key: "listen_port", Type: "integer", Default: &defaultValue,
			}}}
			configuration, secrets, err := normalizeDeploymentConfig(manifest, nil)
			if err != nil {
				t.Fatal(err)
			}
			if string(configuration) != `{"listen_port":1}` {
				t.Fatalf("configuration = %s", configuration)
			}
			if string(secrets) != `{}` {
				t.Fatalf("secrets = %s", secrets)
			}
		})
	}
}

func TestNormalizeDeploymentConfigRejectsNonPortableInteger(t *testing.T) {
	t.Parallel()
	manifest := catalog.AppManifest{Config: []catalog.ConfigField{{
		Key: "listen_port", Type: "integer",
	}}}
	for _, value := range []json.RawMessage{json.RawMessage(`1.5`), json.RawMessage(`9007199254740992`)} {
		value := value
		t.Run(string(value), func(t *testing.T) {
			t.Parallel()
			if _, _, err := normalizeDeploymentConfig(manifest, json.RawMessage(`{"listen_port":`+string(value)+`}`)); err == nil {
				t.Fatal("non-portable integer configuration was accepted")
			}
		})
	}
}

func TestNormalizeDeploymentConfigRejectsNonObjectShapes(t *testing.T) {
	t.Parallel()
	defaultValue := json.RawMessage(`"UTC"`)
	manifest := catalog.AppManifest{Config: []catalog.ConfigField{{
		Key: "timezone", Type: "string", Default: &defaultValue,
	}}}
	for _, value := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`"value"`),
		json.RawMessage(`1`),
		json.RawMessage(`true`),
	} {
		value := value
		t.Run(string(value), func(t *testing.T) {
			t.Parallel()
			if _, _, err := normalizeDeploymentConfig(manifest, value); err == nil {
				t.Fatalf("configuration shape %s was accepted", value)
			}
		})
	}
}

func TestNormalizeDeploymentConfigAllowsOmittedAndEmptyObjects(t *testing.T) {
	t.Parallel()
	defaultValue := json.RawMessage(`"UTC"`)
	manifest := catalog.AppManifest{Config: []catalog.ConfigField{{
		Key: "timezone", Type: "string", Default: &defaultValue,
	}}}
	for _, value := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		configuration, _, err := normalizeDeploymentConfig(manifest, value)
		if err != nil {
			t.Fatal(err)
		}
		if string(configuration) != `{"timezone":"UTC"}` {
			t.Fatalf("configuration = %s", configuration)
		}
	}
}
