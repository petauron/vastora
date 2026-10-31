//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/petauron/vastora/internal/osreinstall"
)

const reinstallReconnectMarker = "/var/lib/vastora/agent/os-reinstall-reconnect.json"

func (c Client) reinstallReconnect(parent context.Context, store *Store) error {
	info, err := os.Lstat(reinstallReconnectMarker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 4096 {
		return errors.New("agent: unsafe reinstall reconnect marker")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 {
		return errors.New("agent: unsafe reinstall reconnect marker")
	}
	raw, err := os.ReadFile(reinstallReconnectMarker)
	if err != nil {
		return err
	}
	var marker osreinstall.ReconnectMarker
	if json.Unmarshal(raw, &marker) != nil || marker.Validate() != nil {
		return errors.New("agent: invalid reinstall reconnect marker")
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	after, err := osreinstall.InspectHost(ctx)
	if err != nil {
		return err
	}
	network, err := osreinstall.CaptureStaticNetworkPlan(ctx, after)
	if err != nil {
		return err
	}
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	report := osreinstall.ReconnectReport{TaskID: marker.TaskID, After: after, Network: network}
	if err = c.CompleteOSReinstall(ctx, connection, marker.ExecutionID, c.executionSession, report); err != nil {
		return err
	}
	// Only remove this single task marker after the authenticated receipt.
	current, err := os.Lstat(reinstallReconnectMarker)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("agent: reinstall marker changed")
	}
	return os.Remove(reinstallReconnectMarker)
}
