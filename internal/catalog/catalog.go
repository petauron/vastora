// Package catalog validates and signs the portable app catalog format.
package catalog

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/platform"
)

const SchemaVersion = 3

const maxPortableInteger int64 = 9_007_199_254_740_991

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	fieldPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	semverPattern     = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	digestPattern     = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)
	sha256Pattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// LocalizedText deliberately requires both v0.1 interface languages.
type LocalizedText struct {
	English           string `json:"en"`
	SimplifiedChinese string `json:"zh-CN"`
}

// ConfigField is an installation-time input. Secret fields are write-only and
// may never define a default value.
type ConfigField struct {
	Key         string           `json:"key"`
	Type        string           `json:"type"`
	Label       LocalizedText    `json:"label"`
	Description LocalizedText    `json:"description"`
	Required    bool             `json:"required"`
	Secret      bool             `json:"secret"`
	Default     *json.RawMessage `json:"default,omitempty"`
}

type Image struct {
	Name      string `json:"name"`
	Reference string `json:"reference"`
}

// Artifact is a signed-catalog native executable. Official typed executors
// select one exact artifact for the Agent host platform and verify it before
// installation; the catalog never carries an executable command line.
type Artifact struct {
	Name            string `json:"name"`
	OperatingSystem string `json:"operatingSystem"`
	Architecture    string `json:"architecture"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
}

// Service declares an addressable application endpoint without coupling the
// catalog to Docker container identities or a gateway implementation.
type Service struct {
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	ContainerPort   int    `json:"containerPort"`
	DefaultHostPort int    `json:"defaultHostPort,omitempty"`
	HostPortField   string `json:"hostPortField,omitempty"`
	HealthPath      string `json:"healthPath,omitempty"`
	Management      bool   `json:"management,omitempty"`
}

// Homepage identifies the service and relative path opened from Center. The
// Agent-reported service endpoint supplies the scheme, address, and port.
type Homepage struct {
	Service string `json:"service"`
	Path    string `json:"path"`
}

// AppManifest is declarative metadata consumed by a typed Agent executor. It
// never carries shell commands or an arbitrary runtime definition.
type AppManifest struct {
	ID          string        `json:"id"`
	Version     string        `json:"version"`
	Name        LocalizedText `json:"name"`
	Description LocalizedText `json:"description"`
	License     string        `json:"license"`
	Images      []Image       `json:"images,omitempty"`
	Artifacts   []Artifact    `json:"artifacts,omitempty"`
	Services    []Service     `json:"services,omitempty"`
	Homepage    *Homepage     `json:"homepage,omitempty"`
	Config      []ConfigField `json:"config"`
	HostAccess  bool          `json:"hostAccess,omitempty"`
}

type Catalog struct {
	SchemaVersion int           `json:"schemaVersion"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Apps          []AppManifest `json:"apps"`
}

// Envelope is the one-file transport representation. Signature covers the
// exact decoded payload bytes, not a re-serialized JSON structure.
type Envelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	KeyID         string `json:"keyId"`
	Payload       string `json:"payload"`
	Signature     string `json:"signature"`
}

func ValidateCatalog(c Catalog) error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("catalog: unsupported schema version %d", c.SchemaVersion)
	}
	if c.GeneratedAt.IsZero() {
		return errors.New("catalog: generatedAt is required")
	}
	seen := make(map[string]struct{}, len(c.Apps))
	for _, app := range c.Apps {
		if err := ValidateApp(app); err != nil {
			return err
		}
		if _, exists := seen[app.ID]; exists {
			return fmt.Errorf("catalog: duplicate app id %q", app.ID)
		}
		seen[app.ID] = struct{}{}
	}
	return nil
}

func ValidateApp(app AppManifest) error {
	if !identifierPattern.MatchString(app.ID) {
		return fmt.Errorf("catalog: invalid app id %q", app.ID)
	}
	if !semverPattern.MatchString(app.Version) {
		return fmt.Errorf("catalog: invalid version for %q", app.ID)
	}
	if err := validateLocalized("name", app.ID, app.Name); err != nil {
		return err
	}
	if err := validateLocalized("description", app.ID, app.Description); err != nil {
		return err
	}
	if strings.TrimSpace(app.License) == "" {
		return fmt.Errorf("catalog: license is required for %q", app.ID)
	}
	if len(app.Images) == 0 && len(app.Artifacts) == 0 {
		return fmt.Errorf("catalog: at least one image or artifact is required for %q", app.ID)
	}
	imageNames := make(map[string]struct{}, len(app.Images))
	for _, image := range app.Images {
		if !identifierPattern.MatchString(image.Name) {
			return fmt.Errorf("catalog: invalid image name %q in %q", image.Name, app.ID)
		}
		if !digestPattern.MatchString(image.Reference) {
			return fmt.Errorf("catalog: image %q in %q must be pinned by sha256 digest", image.Name, app.ID)
		}
		if _, exists := imageNames[image.Name]; exists {
			return fmt.Errorf("catalog: duplicate image %q in %q", image.Name, app.ID)
		}
		imageNames[image.Name] = struct{}{}
	}
	artifactTargets := make(map[string]struct{}, len(app.Artifacts))
	for _, artifact := range app.Artifacts {
		if !identifierPattern.MatchString(artifact.Name) {
			return fmt.Errorf("catalog: invalid artifact name %q in %q", artifact.Name, app.ID)
		}
		if strings.TrimSpace(artifact.OperatingSystem) != artifact.OperatingSystem || strings.TrimSpace(artifact.Architecture) != artifact.Architecture {
			return fmt.Errorf("catalog: artifact %q in %q has a non-canonical platform", artifact.Name, app.ID)
		}
		target, err := platform.Parse(artifact.OperatingSystem, artifact.Architecture)
		if err != nil {
			return fmt.Errorf("catalog: invalid artifact platform for %q in %q: %w", artifact.Name, app.ID, err)
		}
		rawURL := artifact.URL
		parsed, err := url.Parse(rawURL)
		if err != nil || strings.TrimSpace(rawURL) != rawURL || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(rawURL, "?#") {
			return fmt.Errorf("catalog: artifact %q in %q needs an exact credential-free HTTPS URL", artifact.Name, app.ID)
		}
		if !sha256Pattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("catalog: artifact %q in %q must be pinned by sha256 digest", artifact.Name, app.ID)
		}
		key := artifact.Name + "\x00" + target.OS + "\x00" + target.Architecture
		if _, exists := artifactTargets[key]; exists {
			return fmt.Errorf("catalog: duplicate artifact %q for %s/%s in %q", artifact.Name, target.OS, target.Architecture, app.ID)
		}
		artifactTargets[key] = struct{}{}
	}
	fields := make(map[string]string, len(app.Config))
	for _, field := range app.Config {
		if !fieldPattern.MatchString(field.Key) {
			return fmt.Errorf("catalog: invalid field %q in %q", field.Key, app.ID)
		}
		if _, exists := fields[field.Key]; exists {
			return fmt.Errorf("catalog: duplicate field %q in %q", field.Key, app.ID)
		}
		fields[field.Key] = field.Type
		if field.Type != "string" && field.Type != "boolean" && field.Type != "integer" {
			return fmt.Errorf("catalog: unsupported field type %q in %q", field.Type, app.ID)
		}
		if field.Secret && field.Default != nil {
			return fmt.Errorf("catalog: secret field %q in %q cannot define a default", field.Key, app.ID)
		}
		if err := validateConfigDefault(field, app.ID); err != nil {
			return err
		}
		if err := validateLocalized("field label", app.ID, field.Label); err != nil {
			return err
		}
		if err := validateLocalized("field description", app.ID, field.Description); err != nil {
			return err
		}
	}
	services := make(map[string]struct{}, len(app.Services))
	for _, service := range app.Services {
		if !identifierPattern.MatchString(service.Name) {
			return fmt.Errorf("catalog: invalid service name %q in %q", service.Name, app.ID)
		}
		if _, exists := services[service.Name]; exists {
			return fmt.Errorf("catalog: duplicate service %q in %q", service.Name, app.ID)
		}
		services[service.Name] = struct{}{}
		if service.Protocol != "http" && service.Protocol != "https" {
			return fmt.Errorf("catalog: unsupported service protocol %q in %q", service.Protocol, app.ID)
		}
		if service.ContainerPort < 1 || service.ContainerPort > 65535 || service.DefaultHostPort < 0 || service.DefaultHostPort > 65535 {
			return fmt.Errorf("catalog: invalid service port in %q", app.ID)
		}
		if service.HostPortField == "" && service.DefaultHostPort == 0 {
			return fmt.Errorf("catalog: service %q in %q needs a host port", service.Name, app.ID)
		}
		if service.HostPortField != "" && service.DefaultHostPort != 0 {
			return fmt.Errorf("catalog: service %q in %q cannot define both a default host port and a host port field", service.Name, app.ID)
		}
		if service.HostPortField != "" {
			if !fieldPattern.MatchString(service.HostPortField) {
				return fmt.Errorf("catalog: invalid host port field for service %q in %q", service.Name, app.ID)
			}
			fieldType, exists := fields[service.HostPortField]
			if !exists {
				return fmt.Errorf("catalog: service %q in %q references unknown host port field %q", service.Name, app.ID, service.HostPortField)
			}
			if fieldType != "integer" {
				return fmt.Errorf("catalog: service %q in %q needs an integer host port field", service.Name, app.ID)
			}
		}
		if service.HealthPath != "" && (!strings.HasPrefix(service.HealthPath, "/") || strings.HasPrefix(service.HealthPath, "//") || strings.ContainsAny(service.HealthPath, "?#")) {
			return fmt.Errorf("catalog: service %q in %q has an invalid health path", service.Name, app.ID)
		}
	}
	if app.Homepage != nil {
		if _, exists := services[app.Homepage.Service]; !exists {
			return fmt.Errorf("catalog: homepage in %q references unknown service %q", app.ID, app.Homepage.Service)
		}
		if !strings.HasPrefix(app.Homepage.Path, "/") || strings.HasPrefix(app.Homepage.Path, "//") || strings.ContainsAny(app.Homepage.Path, "?#") {
			return fmt.Errorf("catalog: homepage in %q has an invalid path", app.ID)
		}
	}
	return nil
}

func validateConfigDefault(field ConfigField, appID string) error {
	if field.Default == nil {
		return nil
	}
	raw := []byte(*field.Default)
	if string(raw) == "null" {
		return fmt.Errorf("catalog: default for field %q in %q must match type %q", field.Key, appID, field.Type)
	}
	switch field.Type {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return configDefaultTypeError(field, appID)
		}
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return configDefaultTypeError(field, appID)
		}
	case "integer":
		if _, err := NormalizePortableInteger(*field.Default); err != nil {
			return fmt.Errorf("catalog: invalid integer default for field %q in %q: %w", field.Key, appID, err)
		}
	default:
		return nil
	}
	return nil
}

func configDefaultTypeError(field ConfigField, appID string) error {
	return fmt.Errorf("catalog: default for field %q in %q must match type %q", field.Key, appID, field.Type)
}

// NormalizePortableInteger accepts any JSON number whose mathematical value is
// an integer in JavaScript's exactly representable safe range and returns its
// canonical base-10 JSON representation. Catalog consumers use this at both
// contract-validation and deployment boundaries so equivalent lexemes such as
// 1, 1.0, and 1e0 have identical behavior.
func NormalizePortableInteger(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("value must be an integer")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("value must contain one JSON number")
	}
	number, ok := value.(json.Number)
	if !ok {
		return nil, errors.New("value must be an integer")
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() {
		return nil, errors.New("value must be an integer")
	}
	limit := big.NewInt(maxPortableInteger)
	if rational.Num().Cmp(limit) > 0 || rational.Num().Cmp(new(big.Int).Neg(limit)) < 0 {
		return nil, errors.New("integer exceeds the portable safe range")
	}
	return json.RawMessage(rational.Num().String()), nil
}

func validateLocalized(kind, appID string, value LocalizedText) error {
	if strings.TrimSpace(value.English) == "" || strings.TrimSpace(value.SimplifiedChinese) == "" {
		return fmt.Errorf("catalog: %s for %q needs en and zh-CN values", kind, appID)
	}
	return nil
}

func ParseCatalog(payload []byte) (Catalog, error) {
	if err := validateCatalogJSONShape(payload); err != nil {
		return Catalog{}, err
	}
	var c Catalog
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("catalog: decode payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("catalog: payload must contain one JSON value")
	}
	if err := ValidateCatalog(c); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

func validateCatalogJSONShape(payload []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("catalog: decode payload: %w", err)
	}
	if err := rejectJSONNull("catalog", value); err != nil {
		return err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("catalog: payload must be an object")
	}
	appsValue, exists := root["apps"]
	if !exists {
		return errors.New("catalog: apps is required")
	}
	apps, ok := appsValue.([]any)
	if !ok {
		return errors.New("catalog: apps must be an array")
	}
	for appIndex, appValue := range apps {
		app, ok := appValue.(map[string]any)
		if !ok {
			return fmt.Errorf("catalog: app %d must be an object", appIndex)
		}
		configValue, exists := app["config"]
		if !exists {
			return fmt.Errorf("catalog: config is required for app %d", appIndex)
		}
		config, ok := configValue.([]any)
		if !ok {
			return fmt.Errorf("catalog: config for app %d must be an array", appIndex)
		}
		for fieldIndex, fieldValue := range config {
			field, ok := fieldValue.(map[string]any)
			if !ok {
				return fmt.Errorf("catalog: config field %d in app %d must be an object", fieldIndex, appIndex)
			}
			for _, property := range []string{"required", "secret"} {
				if _, exists := field[property]; !exists {
					return fmt.Errorf("catalog: %s is required for config field %d in app %d", property, fieldIndex, appIndex)
				}
			}
		}
		servicesValue, exists := app["services"]
		if !exists {
			continue
		}
		services, ok := servicesValue.([]any)
		if !ok {
			return fmt.Errorf("catalog: services for app %d must be an array", appIndex)
		}
		for serviceIndex, serviceValue := range services {
			service, ok := serviceValue.(map[string]any)
			if !ok {
				return fmt.Errorf("catalog: service %d in app %d must be an object", serviceIndex, appIndex)
			}
			for _, property := range []string{"healthPath", "hostPortField"} {
				if value, exists := service[property]; exists && value == "" {
					return fmt.Errorf("catalog: %s cannot be empty for service %d in app %d", property, serviceIndex, appIndex)
				}
			}
			if value, exists := service["defaultHostPort"]; exists {
				if number, ok := value.(json.Number); ok {
					parsed, parseErr := number.Float64()
					if parseErr == nil && parsed == 0 {
						return fmt.Errorf("catalog: defaultHostPort cannot be zero for service %d in app %d", serviceIndex, appIndex)
					}
				}
			}
		}
	}
	return nil
}

func rejectJSONNull(path string, value any) error {
	if value == nil {
		return fmt.Errorf("catalog: null is not allowed at %s", path)
	}
	switch value := value.(type) {
	case []any:
		for index, item := range value {
			if err := rejectJSONNull(fmt.Sprintf("%s[%d]", path, index), item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range value {
			if err := rejectJSONNull(path+"."+key, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func MarshalCatalog(c Catalog) ([]byte, error) {
	if err := ValidateCatalog(c); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func Sign(keyID string, privateKey ed25519.PrivateKey, payload []byte) (Envelope, error) {
	if strings.TrimSpace(keyID) == "" {
		return Envelope{}, errors.New("catalog: key id is required")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("catalog: invalid Ed25519 private key")
	}
	if _, err := ParseCatalog(payload); err != nil {
		return Envelope{}, err
	}
	return Envelope{
		SchemaVersion: SchemaVersion,
		KeyID:         keyID,
		Payload:       base64.RawURLEncoding.EncodeToString(payload),
		Signature:     base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}, nil
}

func Verify(envelope Envelope, publicKey ed25519.PublicKey) (Catalog, []byte, error) {
	if envelope.SchemaVersion != SchemaVersion {
		return Catalog{}, nil, fmt.Errorf("catalog: unsupported envelope schema version %d", envelope.SchemaVersion)
	}
	if strings.TrimSpace(envelope.KeyID) == "" {
		return Catalog{}, nil, errors.New("catalog: envelope key id is required")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return Catalog{}, nil, errors.New("catalog: invalid Ed25519 public key")
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return Catalog{}, nil, fmt.Errorf("catalog: decode payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return Catalog{}, nil, fmt.Errorf("catalog: decode signature: %w", err)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return Catalog{}, nil, errors.New("catalog: signature verification failed")
	}
	catalog, err := ParseCatalog(payload)
	if err != nil {
		return Catalog{}, nil, err
	}
	return catalog, payload, nil
}

func MarshalEnvelope(envelope Envelope) ([]byte, error) {
	return json.MarshalIndent(envelope, "", "  ")
}

func ParseEnvelope(raw []byte) (Envelope, error) {
	var envelope Envelope
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("catalog: decode envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Envelope{}, errors.New("catalog: envelope must contain one JSON value")
	}
	return envelope, nil
}

// AppKeys sorts app identifiers for stable API output and tests.
func AppKeys(c Catalog) []string {
	keys := make([]string, 0, len(c.Apps))
	for _, app := range c.Apps {
		keys = append(keys, app.ID)
	}
	sort.Strings(keys)
	return keys
}
