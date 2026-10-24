package center

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var errRenewalSessionInvalid = errors.New("center: session is invalid or expired")

// renewSession does not mint identities or tokens. Revocation remains a deletion;
// a concurrent logout/password change can never be undone by renewal.
func (s *Store) renewSession(ctx context.Context, token string) (string, time.Time, error) {
	var csrf, expiry, absolute string
	if err := s.db.QueryRowContext(ctx, `SELECT csrf_token, expires_at, absolute_expires_at FROM sessions WHERE token_hash=?`, tokenHash(token)).Scan(&csrf, &expiry, &absolute); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, errRenewalSessionInvalid
		}
		return "", time.Time{}, err
	}
	now := s.now().UTC()
	expires, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !expires.After(now) {
		return "", time.Time{}, errRenewalSessionInvalid
	}
	deadline, err := time.Parse(time.RFC3339Nano, absolute)
	if err != nil || !deadline.After(now) {
		return "", time.Time{}, errRenewalSessionInvalid
	}
	if expires.Sub(now) > sessionRenewalWindow {
		return "", time.Time{}, nil
	}
	next := now.Add(sessionLifetime)
	if next.After(deadline) {
		next = deadline
	}
	if !next.After(expires) {
		return "", time.Time{}, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at=? WHERE token_hash=? AND expires_at=? AND absolute_expires_at=?`, next.Format(time.RFC3339Nano), tokenHash(token), expiry, absolute)
	if err != nil {
		return "", time.Time{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", time.Time{}, err
	}
	if changed == 0 {
		// Another request may have renewed, or the session may have been revoked.
		return "", time.Time{}, s.ValidateSession(ctx, token, "", false)
	}
	return csrf, next, nil
}
