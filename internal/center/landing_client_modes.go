package center

import (
	"encoding/json"
	"errors"
)

// Confirm active identity ownership before parent metadata can be used for a
// newly requested grant; a cached row never supplies an authentication secret.
func landingAccountMetadata(raw []byte, id string) (ThreeXUIClientView, error) {
	var client ThreeXUIClientView
	if json.Unmarshal(raw, &client) != nil || client.ID != id {
		return client, errors.New("center: invalid client identity")
	}
	return client, nil
}
