//go:build !linux

package agent

import (
	"context"
	"errors"

	"github.com/petauron/vastora/internal/nodeswap"
)

func (s *Store) executeNodeSwap(context.Context, string, nodeswap.Task) (nodeswap.View, error) {
	return nodeswap.View{}, errors.New("swap: Linux systemd KVM hosts only")
}

func CheckHostSwapRemoval(string) error { return nil }
