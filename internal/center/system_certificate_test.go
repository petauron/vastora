package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func storeSystemCenterCertificateForTest(t *testing.T, store *Store, hostname string) managedCertificate {
	t.Helper()
	certificate := testManagedCertificate(t, hostname)
	notAfter := certificate.NotAfter
	encoded, err := json.Marshal(certificate)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := store.putSecret(context.Background(), tx, encoded, systemCenterCertificateContext)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		systemCenterCertificateSecretSetting: secretID,
		systemCenterCertificateExpirySetting: notAfter.Format(time.RFC3339Nano),
	} {
		if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return certificate
}
