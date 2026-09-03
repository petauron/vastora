package center

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	adminPasswordMinLength = 10
	sessionLifetime        = 24 * time.Hour
	loginFailureWindow     = 15 * time.Minute
	loginLockoutDuration   = 15 * time.Minute
	loginFailureRetention  = 24 * time.Hour
	loginMaxFailures       = 5
)

type LoginThrottle struct {
	RetryAfter time.Duration
}

func (s *Store) CreateFirstAdmin(ctx context.Context, username, password string) (string, string, error) {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return "", "", errors.New("center: username must be 3 to 64 characters using letters, numbers, dots, underscores, or hyphens")
	}
	if utf8.RuneCountInString(password) < adminPasswordMinLength {
		return "", "", errors.New("center: password must be at least 10 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("center: begin setup: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return "", "", fmt.Errorf("center: read administrators: %w", err)
	}
	if count != 0 {
		return "", "", errors.New("center: setup is already complete")
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return "", "", err
	}
	adminID, err := randomToken(18)
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO admins(id, username, password_hash, created_at) VALUES(?, ?, ?, ?)`, adminID, username, passwordHash, now.Format(time.RFC3339Nano)); err != nil {
		return "", "", fmt.Errorf("center: create administrator: %w", err)
	}
	sessionToken, csrfToken, err := createSession(ctx, tx, adminID, now)
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("center: commit setup: %w", err)
	}
	return sessionToken, csrfToken, nil
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (string, string, error) {
	username = strings.TrimSpace(username)
	var adminID, passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM admins WHERE username = ?`, username).Scan(&adminID, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same Argon2 work for an unknown username so the response does
		// not reveal whether an administrator exists.
		verifyPassword(password, dummyAdminPasswordHash)
		return "", "", errors.New("center: sign-in failed")
	}
	if err != nil {
		return "", "", fmt.Errorf("center: read administrator: %w", err)
	}
	if !verifyPassword(password, passwordHash) {
		return "", "", errors.New("center: sign-in failed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("center: begin session: %w", err)
	}
	defer tx.Rollback()
	token, csrf, err := createSession(ctx, tx, adminID, s.now().UTC())
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("center: commit session: %w", err)
	}
	return token, csrf, nil
}

func (s *Store) LoginThrottle(ctx context.Context, username, clientAddress string) (LoginThrottle, error) {
	now := s.now().UTC()
	keys := s.loginFailureKeys(username, clientAddress)
	var blockedUntil time.Time
	for scope, key := range keys {
		var raw string
		err := s.db.QueryRowContext(ctx, `SELECT blocked_until FROM login_failures WHERE scope = ? AND key_hash = ?`, scope, key).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return LoginThrottle{}, fmt.Errorf("center: read login throttle: %w", err)
		}
		value, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return LoginThrottle{}, errors.New("center: stored login throttle is invalid")
		}
		if value.After(blockedUntil) {
			blockedUntil = value
		}
	}
	if blockedUntil.After(now) {
		return LoginThrottle{RetryAfter: blockedUntil.Sub(now)}, nil
	}
	return LoginThrottle{}, nil
}

func (s *Store) RecordLoginFailure(ctx context.Context, username, clientAddress string, includeAccount bool) (LoginThrottle, error) {
	now := s.now().UTC()
	keys := s.loginFailureKeys(username, clientAddress)
	if !includeAccount {
		delete(keys, "account")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginThrottle{}, fmt.Errorf("center: begin login throttle update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_failures WHERE updated_at < ?`, now.Add(-loginFailureRetention).Format(time.RFC3339Nano)); err != nil {
		return LoginThrottle{}, fmt.Errorf("center: expire login throttle: %w", err)
	}
	var longest time.Duration
	for scope, key := range keys {
		count := 0
		windowStarted := now
		var rawWindow string
		err := tx.QueryRowContext(ctx, `SELECT failed_count, window_started_at FROM login_failures WHERE scope = ? AND key_hash = ?`, scope, key).Scan(&count, &rawWindow)
		if err == nil {
			parsed, parseErr := time.Parse(time.RFC3339Nano, rawWindow)
			if parseErr != nil {
				return LoginThrottle{}, errors.New("center: stored login throttle is invalid")
			}
			windowStarted = parsed
			if !now.Before(windowStarted.Add(loginFailureWindow)) {
				count = 0
				windowStarted = now
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return LoginThrottle{}, fmt.Errorf("center: read login failure count: %w", err)
		}
		count++
		delay := loginBackoff(count)
		if count >= loginMaxFailures {
			delay = loginLockoutDuration
			windowStarted = now
		}
		if delay > longest {
			longest = delay
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO login_failures(scope, key_hash, failed_count, window_started_at, blocked_until, updated_at)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope, key_hash) DO UPDATE SET failed_count = excluded.failed_count, window_started_at = excluded.window_started_at,
			blocked_until = excluded.blocked_until, updated_at = excluded.updated_at`, scope, key, count, windowStarted.Format(time.RFC3339Nano), now.Add(delay).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return LoginThrottle{}, fmt.Errorf("center: record login failure: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return LoginThrottle{}, fmt.Errorf("center: commit login throttle update: %w", err)
	}
	return LoginThrottle{RetryAfter: longest}, nil
}

func (s *Store) ClearLoginFailures(ctx context.Context, username, clientAddress string) error {
	keys := s.loginFailureKeys(username, clientAddress)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin login throttle reset: %w", err)
	}
	defer tx.Rollback()
	for scope, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_failures WHERE scope = ? AND key_hash = ?`, scope, key); err != nil {
			return fmt.Errorf("center: clear login throttle: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) loginFailureKeys(username, clientAddress string) map[string]string {
	return map[string]string{
		"account": s.loginFailureKey("account", strings.ToLower(strings.TrimSpace(username))),
		"client":  s.loginFailureKey("client", strings.TrimSpace(clientAddress)),
	}
}

func (s *Store) loginFailureKey(scope, value string) string {
	digest := hmac.New(sha256.New, s.key)
	_, _ = digest.Write([]byte("vastora-login-" + scope + "\x00" + value))
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func loginBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 5 {
		failures = 5
	}
	return time.Duration(1<<failures) * time.Second
}

func (s *Store) ValidateSession(ctx context.Context, sessionToken, csrfToken string, mutation bool) error {
	if sessionToken == "" {
		return errors.New("center: authentication required")
	}
	var storedCSRF, expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT csrf_token, expires_at FROM sessions WHERE token_hash = ?`, tokenHash(sessionToken)).Scan(&storedCSRF, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: session is invalid")
	}
	if err != nil {
		return fmt.Errorf("center: read session: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: session has expired")
	}
	if mutation && subtle.ConstantTimeCompare([]byte(storedCSRF), []byte(csrfToken)) != 1 {
		return errors.New("center: CSRF token is invalid")
	}
	return nil
}

func (s *Store) SessionAdminID(ctx context.Context, sessionToken string) (string, error) {
	if sessionToken == "" {
		return "", errors.New("center: authentication required")
	}
	var adminID, expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT admin_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash(sessionToken)).Scan(&adminID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: session is invalid")
	}
	if err != nil {
		return "", fmt.Errorf("center: read session identity: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return "", errors.New("center: session has expired")
	}
	return adminID, nil
}

func (s *Store) ReauthenticateAdmin(ctx context.Context, adminID, password string) error {
	adminID = strings.TrimSpace(adminID)
	if adminID == "" || password == "" {
		return errors.New("center: current password is incorrect")
	}
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admins WHERE id = ?`, adminID).Scan(&passwordHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !verifyPassword(password, passwordHash)) {
		return errors.New("center: current password is incorrect")
	}
	if err != nil {
		return fmt.Errorf("center: read administrator: %w", err)
	}
	return nil
}

func (s *Store) Logout(ctx context.Context, sessionToken string) error {
	if sessionToken == "" {
		return errors.New("center: authentication required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(sessionToken))
	if err != nil {
		return fmt.Errorf("center: revoke session: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("center: revoke session: %w", err)
	}
	if changed != 1 {
		return errors.New("center: session is invalid")
	}
	return nil
}

func (s *Store) ChangePassword(ctx context.Context, sessionToken, currentPassword, newPassword string) error {
	if sessionToken == "" {
		return errors.New("center: authentication required")
	}
	if utf8.RuneCountInString(newPassword) < adminPasswordMinLength {
		return errors.New("center: new password must be at least 10 characters")
	}
	if currentPassword == newPassword {
		return errors.New("center: new password must be different")
	}
	var adminID, passwordHash, expiresAt string
	sessionHash := tokenHash(sessionToken)
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.password_hash, s.expires_at FROM sessions s JOIN admins a ON a.id = s.admin_id WHERE s.token_hash = ?`, sessionHash).Scan(&adminID, &passwordHash, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: session is invalid")
	}
	if err != nil {
		return fmt.Errorf("center: read administrator session: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: session has expired")
	}
	if !verifyPassword(currentPassword, passwordHash) {
		return errors.New("center: current password is incorrect")
	}
	nextHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin password change: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE admins SET password_hash = ? WHERE id = ? AND password_hash = ?`, nextHash, adminID, passwordHash)
	if err != nil {
		return fmt.Errorf("center: update administrator password: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: administrator password changed concurrently; retry")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ? AND token_hash <> ?`, adminID, sessionHash); err != nil {
		return fmt.Errorf("center: revoke other sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit password change: %w", err)
	}
	return nil
}

func createSession(ctx context.Context, tx *sql.Tx, adminID string, now time.Time) (string, string, error) {
	sessionToken, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	csrfToken, err := randomToken(24)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash, csrf_token, admin_id, expires_at) VALUES(?, ?, ?, ?)`, tokenHash(sessionToken), csrfToken, adminID, now.Add(sessionLifetime).Format(time.RFC3339Nano)); err != nil {
		return "", "", fmt.Errorf("center: create session: %w", err)
	}
	return sessionToken, csrfToken, nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("center: generate password salt: %w", err)
	}
	encoded := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return "argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(encoded), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" || parts[2] != "m=65536,t=3,p=2" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

// A valid Argon2id encoding with a zero digest. It deliberately never matches,
// but still forces the complete password derivation for unknown usernames.
const dummyAdminPasswordHash = "argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
