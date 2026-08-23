package center

import (
	"testing"
	"time"
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
