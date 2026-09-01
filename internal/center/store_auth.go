package center

import (
	"context"
	"crypto/rand"
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
)

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
		return "", "", errors.New("center: invalid credentials")
	}
	if err != nil {
		return "", "", fmt.Errorf("center: read administrator: %w", err)
	}
	if !verifyPassword(password, passwordHash) {
		return "", "", errors.New("center: invalid credentials")
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
