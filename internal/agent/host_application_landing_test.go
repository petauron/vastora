package agent

import (
	"bytes"
	"testing"
)

func TestLandingCommandOutputIsBounded(t *testing.T) {
	var output landingCommandOutput
	data := bytes.Repeat([]byte("x"), 100<<10)
	for range 3 {
		written, err := output.Write(data)
		if err != nil || written != len(data) {
			t.Fatal("bounded output interrupted the child process")
		}
	}
	if output.Len() != 64<<10 {
		t.Fatal("command output exceeded its memory limit")
	}
}

func TestLandingListenerIdentity(t *testing.T) {
	data := []byte("  sl local_address rem_address st tx_queue rx tr tm retr uid timeout inode\n 0: 01004064:0438 00000000:0000 0A 00000000:00000000 00:00000000 00000000 991 0 1257\n")
	if !landingListenerPresent(data, "100.64.0.1", 991) {
		t.Fatal("owned private listener was not recognized")
	}
	for _, test := range []struct {
		address string
		uid     uint32
	}{
		{"100.64.0.2", 991}, {"100.64.0.1", 992}, {"::1", 991}, {"invalid", 991},
	} {
		if landingListenerPresent(data, test.address, test.uid) {
			t.Fatal("unrelated listener accepted")
		}
	}
	if landingListenerPresent([]byte("truncated"), "100.64.0.1", 991) {
		t.Fatal("invalid kernel row accepted")
	}
}
