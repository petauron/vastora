package center

import "sync"

// changeNotifier provides process-local wakeups while SQLite remains the
// durable source of truth. Callers always query after subscribing and recheck
// again when their bounded request is renewed.
type changeNotifier struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func (n *changeNotifier) subscribe(key string) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.waiters == nil {
		n.waiters = make(map[string]map[chan struct{}]struct{})
	}
	if n.waiters[key] == nil {
		n.waiters[key] = make(map[chan struct{}]struct{})
	}
	waiter := make(chan struct{})
	n.waiters[key][waiter] = struct{}{}
	return waiter
}

func (n *changeNotifier) unsubscribe(key string, waiter <-chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	waiters := n.waiters[key]
	for candidate := range waiters {
		if candidate == waiter {
			delete(waiters, candidate)
			break
		}
	}
	if len(waiters) == 0 {
		delete(n.waiters, key)
	}
}

func (n *changeNotifier) notify(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.waiters == nil {
		return
	}
	waiters, exists := n.waiters[key]
	if !exists {
		return
	}
	delete(n.waiters, key)
	for waiter := range waiters {
		close(waiter)
	}
}
