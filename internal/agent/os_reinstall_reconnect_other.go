//go:build !linux

package agent

import "context"

func (c Client) reinstallReconnect(context.Context, *Store) error { return nil }
