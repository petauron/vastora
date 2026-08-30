package center

import (
	"context"
	"strings"
	"sync"
	"testing"
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
