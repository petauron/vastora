//go:build !linux

package agent

func (*Store) StartTCPBudget() (func(), error) { return func() {}, nil }
