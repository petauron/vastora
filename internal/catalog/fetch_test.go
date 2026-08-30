package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestFetchRejectsSourceURLCredentialsBeforeNetworkAccess(t *testing.T) {
	_, err := Fetch(context.Background(), FetchConfig{URL: "https://catalog-user:catalog-password@example.invalid/catalog.json"})
	if err == nil || !strings.Contains(err.Error(), "without credentials") {
		t.Fatalf("catalog source URL credentials were accepted: %v", err)
	}
}
