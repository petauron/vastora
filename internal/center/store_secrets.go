package center

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

func (s *Store) putSecret(ctx context.Context, tx *sql.Tx, value []byte, additionalData string) (string, error) {
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	sealed, err := secret.Seal(s.key, value, []byte(additionalData))
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO secrets(id, sealed, created_at, updated_at) VALUES(?, ?, ?, ?)`, id, sealed, now, now); err != nil {
		return "", fmt.Errorf("center: save secret: %w", err)
	}
	return id, nil
}

func (s *Store) getSecret(ctx context.Context, id, additionalData string) ([]byte, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, id).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("center: read secret: %w", err)
	}
	value, err := secret.Open(s.key, sealed, []byte(additionalData))
	if err != nil {
		return nil, fmt.Errorf("center: decrypt secret: %w", err)
	}
	return value, nil
}

func tokenHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("center: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomDNSLabel(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("center: generate random DNS label: %w", err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)), nil
}
