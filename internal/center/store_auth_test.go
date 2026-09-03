package center

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdministratorPasswordMinimumLength(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if _, _, err := store.CreateFirstAdmin(ctx, "admin", "123456789"); err == nil {
		t.Fatal("nine-character administrator password was accepted")
	}
	session, _, err := store.CreateFirstAdmin(ctx, "admin", "1234567890")
	if err != nil {
		t.Fatalf("ten-character administrator password was rejected: %v", err)
	}
	if err := store.ChangePassword(ctx, session, "1234567890", "管理员密码共十位字符"); err != nil {
		t.Fatalf("ten-character Unicode administrator password was rejected: %v", err)
	}
	if _, _, err := store.Authenticate(ctx, "admin", "管理员密码共十位字符"); err != nil {
		t.Fatalf("administrator could not authenticate with the changed password: %v", err)
	}
}

func TestConcurrentBootstrapCreatesExactlyOneArgon2idAdministrator(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _, createErr := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
			results <- createErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	for result := range results {
		if result == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent bootstrap successes = %d, want 1", succeeded)
	}
	var administrators, sessions int
	var passwordHash string
	if err := store.db.QueryRow(`SELECT COUNT(*), MIN(password_hash) FROM admins`).Scan(&administrators, &passwordHash); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if administrators != 1 || sessions != 1 {
		t.Fatalf("bootstrap persisted administrators=%d sessions=%d", administrators, sessions)
	}
	if !strings.HasPrefix(passwordHash, "argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("administrator password hash has an unexpected format: %q", passwordHash)
	}
	if strings.Contains(passwordHash, "correct-horse-battery-staple") {
		t.Fatal("administrator password was stored in plaintext")
	}
}

func TestLoginFailuresPersistBackoffAndLockoutWithoutStoringIdentifiers(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }

	expected := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 15 * time.Minute}
	for index, want := range expected {
		throttle, err := store.RecordLoginFailure(context.Background(), "Admin", "203.0.113.9", true)
		if err != nil {
			t.Fatal(err)
		}
		if throttle.RetryAfter != want {
			t.Fatalf("failure %d retry delay = %s, want %s", index+1, throttle.RetryAfter, want)
		}
	}
	var rows int
	var storedKeys string
	if err := store.db.QueryRow(`SELECT COUNT(*), GROUP_CONCAT(key_hash, ',') FROM login_failures`).Scan(&rows, &storedKeys); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || strings.Contains(strings.ToLower(storedKeys), "admin") || strings.Contains(storedKeys, "203.0.113.9") {
		t.Fatalf("login throttle stored unexpected identifiers: rows=%d keys=%q", rows, storedKeys)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return fixed }
	throttle, err := reopened.LoginThrottle(context.Background(), "admin", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if throttle.RetryAfter != 15*time.Minute {
		t.Fatalf("persistent lockout = %s, want 15m", throttle.RetryAfter)
	}
	if err := reopened.ClearLoginFailures(context.Background(), "ADMIN", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	throttle, err = reopened.LoginThrottle(context.Background(), "admin", "203.0.113.9")
	if err != nil || throttle.RetryAfter != 0 {
		t.Fatalf("successful-login reset left throttle=%s err=%v", throttle.RetryAfter, err)
	}
}
