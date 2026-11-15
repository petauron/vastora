//go:build !linux

package osreinstall

func readNetworkCandidate(Network, []byte) string { return "" }
