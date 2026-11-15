package agent

import "time"

// NewCenterControlClient creates the update helper's independent control
// session. The first verified peer is pinned for the helper's lifetime.
func NewCenterControlClient(connection Connection, timeout time.Duration) (Client, func(), error) {
	session, err := newGRPCControlSession(connection)
	if err != nil {
		return Client{}, nil, err
	}
	if timeout > 0 {
		session.timeout = timeout
	}
	return Client{centerRPC: session}, session.close, nil
}
