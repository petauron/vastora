package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// encoding/json accepts duplicate properties and matches struct fields without
// regard to case. Neither is safe for a signed portable format: another consumer
// can interpret the same signed bytes differently. Keep the standard parser for
// JSON syntax, but check property identity before it discards that information.
// The existing schema/canonical-JSON dependencies operate on already parsed
// objects and therefore cannot recover duplicate input properties.
func decodeStrictJSON(raw []byte, destination any) error {
	reader := json.NewDecoder(bytes.NewReader(raw))
	reader.UseNumber()
	if err := checkJSONValue(reader, reflect.TypeOf(destination), 0); err != nil {
		return err
	}
	if _, err := reader.Token(); err != io.EOF {
		return errors.New("JSON must contain one value")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func checkJSONValue(reader *json.Decoder, expected reflect.Type, depth int) error {
	if depth > 64 {
		return errors.New("JSON exceeds the catalog nesting limit")
	}
	for expected != nil && expected.Kind() == reflect.Pointer {
		expected = expected.Elem()
	}
	// RawMessage and time.Time have their own JSON representation. Their
	// contents still pass the duplicate-key check, without treating their Go
	// implementation fields as part of the portable schema.
	if expected != nil && reflect.PointerTo(expected).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		expected = nil
	}
	token, err := reader.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for reader.More() {
			property, err := reader.Token()
			if err != nil {
				return err
			}
			name, ok := property.(string)
			if !ok || seen[name] {
				return errors.New("JSON contains a duplicate or invalid object property")
			}
			seen[name] = true
			var child reflect.Type
			if expected != nil && expected.Kind() == reflect.Struct {
				for index := 0; index < expected.NumField(); index++ {
					field := expected.Field(index)
					if field.IsExported() && strings.Split(field.Tag.Get("json"), ",")[0] == name {
						child = field.Type
						break
					}
				}
				if child == nil {
					return fmt.Errorf("JSON contains unknown property %q", name)
				}
			}
			if err := checkJSONValue(reader, child, depth+1); err != nil {
				return err
			}
		}
		_, err = reader.Token() // The decoder validates the matching closing delimiter.
		return err
	case json.Delim('['):
		var child reflect.Type
		if expected != nil && (expected.Kind() == reflect.Slice || expected.Kind() == reflect.Array) {
			child = expected.Elem()
		}
		for reader.More() {
			if err := checkJSONValue(reader, child, depth+1); err != nil {
				return err
			}
		}
		_, err = reader.Token()
		return err
	default:
		return nil // The typed decoder checks primitive types after this scan.
	}
}
