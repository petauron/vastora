package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestChangeNotifierWakesEverySubscriber(t *testing.T) {
	var notifier changeNotifier
	first := notifier.subscribe("agent:one")
	second := notifier.subscribe("agent:one")
	notifier.notify("agent:one")
	for index, waiter := range []<-chan struct{}{first, second} {
		select {
		case <-waiter:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("subscriber %d was not notified", index)
		}
	}
}

func TestTaskEventNotificationsAreEmittedOnlyAfterCommit(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "commit-test-agent", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.61", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", EnabledKinds: []string{networking.KindLAN}})
	agentWaiter := store.taskChanges.subscribe("agent:" + node.ID)
	taskWaiter := store.taskChanges.subscribe("task:commit-test-task")

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.recordTaskEvent(ctx, tx, "commit-test-task", node.ID, "application.command", 1, "queued", "queued in transaction"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	select {
	case <-agentWaiter:
		_ = tx.Rollback()
		t.Fatal("Agent was notified before the task event committed")
	case <-taskWaiter:
		_ = tx.Rollback()
		t.Fatal("task event stream was notified before the task event committed")
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for name, waiter := range map[string]<-chan struct{}{"agent": agentWaiter, "task": taskWaiter} {
		select {
		case <-waiter:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s waiter was not notified after commit", name)
		}
	}
}

func TestRolledBackTaskEventDoesNotNotifyWaiters(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "rollback-test-agent", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.62", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.62", LANAddress: "10.0.0.62", EnabledKinds: []string{networking.KindLAN}})
	waiter := store.taskChanges.subscribe("task:rollback-test-task")
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.recordTaskEvent(ctx, tx, "rollback-test-task", node.ID, "application.command", 1, "queued", "rolled back"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiter:
		t.Fatal("rolled-back task event notified a waiter")
	case <-time.After(100 * time.Millisecond):
		store.taskChanges.unsubscribe("task:rollback-test-task", waiter)
	}
}
