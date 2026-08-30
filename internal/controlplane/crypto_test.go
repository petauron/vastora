package controlplane

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTripBindsAgentTaskAndAttempt(t *testing.T) {
	privateKey, publicKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	aad := TaskAdditionalData("agent-1", "task-1", 2)
	envelope, err := Seal(publicKey, []byte(`{"secret":"value"}`), aad)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := Open(privateKey, envelope, aad)
	if err != nil || !bytes.Equal(plaintext, []byte(`{"secret":"value"}`)) {
		t.Fatalf("round trip plaintext=%q err=%v", plaintext, err)
	}
	if _, err := Open(privateKey, envelope, TaskAdditionalData("agent-1", "task-1", 3)); err == nil {
		t.Fatal("encrypted task was accepted for a different lease attempt")
	}
	envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 1
	if _, err := Open(privateKey, envelope, aad); err == nil {
		t.Fatal("tampered encrypted task was accepted")
	}
}

func TestEnvelopeRejectsOversizedPayload(t *testing.T) {
	_, publicKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(publicKey, make([]byte, MaxTaskPayload+1), []byte("aad")); err == nil {
		t.Fatal("oversized task payload was accepted")
	}
}
