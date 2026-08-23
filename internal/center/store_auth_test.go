package center

import (
	"context"
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
