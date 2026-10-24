package center

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionRenewalBoundariesAndRevocation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	s.now = func() time.Time { return now }
	token, csrf, err := s.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, expiry, err := s.renewSession(ctx, token); err != nil || !expiry.IsZero() {
		t.Fatalf("premature renewal: %v %v", expiry, err)
	}
	server := &Server{store: s, secureCookies: true}
	request := func(csrfHeader string, mutate bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://example.test/api/v1/test", nil)
		r.AddCookie(&http.Cookie{Name: "vastora_session", Value: token})
		r.Header.Set("X-CSRF-Token", csrfHeader)
		w := httptest.NewRecorder()
		server.requireAuth(mutate, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })(w, r)
		return w
	}
	now = start.Add(13 * time.Hour)
	if w := request("wrong", true); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("invalid CSRF renewed")
	}
	w := request(csrf, true)
	if w.Code != 204 {
		t.Fatalf("renewal response %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies: %d", len(cookies))
	}
	for _, c := range cookies {
		if !c.Expires.Equal(now.Add(sessionLifetime)) || !c.Secure || c.SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie properties: %s", c.Name)
		}
	}
	if !cookies[0].HttpOnly || cookies[0].Value != token || cookies[1].Value != csrf {
		t.Fatal("identity/CSRF changed")
	}
	// Repeated activity extends only to the fixed absolute limit.
	for hours := 26; hours < 168; hours += 13 {
		now = start.Add(time.Duration(hours) * time.Hour)
		if _, _, err := s.renewSession(ctx, token); err != nil {
			t.Fatal(err)
		}
	}
	var expiry string
	if err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash=?`, tokenHash(token)).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if expiry != start.Add(sessionMaximumLifetime).Format(time.RFC3339Nano) {
		t.Fatalf("absolute deadline %s", expiry)
	}
	now = start.Add(sessionMaximumLifetime)
	if w := request(csrf, false); w.Code != 401 {
		t.Fatal("absolute expiry accepted")
	}
	token, _, err = s.Authenticate(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.Authenticate(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, token, "correct-horse-battery-staple", "new-correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.renewSession(ctx, other); err == nil {
		t.Fatal("password-revoked session resurrected")
	}
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.renewSession(ctx, token); err == nil {
		t.Fatal("logout session resurrected")
	}
}

func TestLegacySessionRenewalMigrationPreservesDeadline(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	token, _, err := s.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the released-table migration directly, without changing history.
	if _, err := s.db.Exec(`ALTER TABLE sessions DROP COLUMN absolute_expires_at`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrationFiles.ReadFile("migrations/00119_session_renewal.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var before, absolute string
	if err := s.db.QueryRow(`SELECT expires_at,absolute_expires_at FROM sessions`).Scan(&before, &absolute); err != nil {
		t.Fatal(err)
	}
	if before != absolute {
		t.Fatal("legacy deadline extended")
	}
	deadline, _ := time.Parse(time.RFC3339Nano, before)
	s.now = func() time.Time { return deadline.Add(-time.Hour) }
	if _, expiry, err := s.renewSession(ctx, token); err != nil || !expiry.IsZero() {
		t.Fatalf("legacy extended: %v %v", expiry, err)
	}
	s.now = func() time.Time { return deadline }
	if _, _, err := s.renewSession(ctx, token); err == nil {
		t.Fatal("expired session revived")
	}
}
