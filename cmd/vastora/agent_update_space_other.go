//go:build !linux && !darwin

package main

import "errors"

func requireAgentUpdateSpace(string, int64) error {
	return errors.New("agent: host update space checks require a supported Unix host")
}
