package center

import (
	"encoding/json"
	"errors"

	"github.com/petauron/catalog/catalog"
)

// A native bundle may update declared service-port metadata without replacing
// application-owned files only if the changed fields are not runtime inputs.
// The Agent independently verifies the installed recipe, files and listeners.
func validateNativeServicePortConfigure(manifest catalog.AppManifest, raw json.RawMessage) error {
	invalid := errors.New("center: native configure supports only declared TCP service ports that are not runtime inputs")
	var changed map[string]json.RawMessage
	if json.Unmarshal(raw, &changed) != nil || len(changed) == 0 || manifest.Runtime == nil {
		return invalid
	}
	allowed := map[string]bool{}
	for _, service := range manifest.Services {
		if service.HostPortField == "" {
			continue
		}
		if service.Protocol == "tcp" || service.Protocol == "http" || service.Protocol == "https" {
			allowed[service.HostPortField] = true
		}
	}
	for _, service := range manifest.Services {
		if service.HostPortField != "" && service.Protocol != "tcp" && service.Protocol != "http" && service.Protocol != "https" {
			delete(allowed, service.HostPortField)
		}
	}
	encoded, err := json.Marshal(manifest.Runtime)
	if err != nil {
		return invalid
	}
	var runtime any
	if json.Unmarshal(encoded, &runtime) != nil {
		return invalid
	}
	var references func(any, string) bool
	references = func(value any, field string) bool {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "config" {
					if name, ok := child.(string); ok && name == field {
						return true
					}
				}
				if references(child, field) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if references(child, field) {
					return true
				}
			}
		}
		return false
	}
	for field, rawPort := range changed {
		var port int
		if !allowed[field] || json.Unmarshal(rawPort, &port) != nil || port < 1 || port > 65535 || references(runtime, field) {
			return invalid
		}
	}
	return nil
}
