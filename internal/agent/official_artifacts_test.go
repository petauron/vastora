package agent

import (
	"encoding/binary"
	"testing"
)

func testArtifactELF(t *testing.T, architecture string) []byte {
	t.Helper()
	// Minimal ELF64 headers; no code is executed by the verifier.
	header := make([]byte, 64)
	copy(header, []byte("\x7fELF"))
	header[4], header[5], header[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(header[16:18], 2)
	machine, ok := map[string]uint16{"amd64": 62, "arm64": 183}[architecture]
	if !ok {
		t.Fatalf("unknown test architecture %q", architecture)
	}
	binary.LittleEndian.PutUint16(header[18:20], machine)
	binary.LittleEndian.PutUint32(header[20:24], 1)
	binary.LittleEndian.PutUint16(header[52:54], 64)
	return header
}

func TestArtifactELFPlatformVerification(t *testing.T) {
	header := testArtifactELF(t, "amd64")
	if err := verifyArtifactELF(header, "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifactELF(header, "arm64"); err == nil {
		t.Fatal("wrong architecture accepted")
	}
	binary.LittleEndian.PutUint16(header[18:20], 183)
	if err := verifyArtifactELF(header, "arm64"); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifactELF(header, "unknown"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
	if err := verifyArtifactELF([]byte("not an executable"), "amd64"); err == nil {
		t.Fatal("non-ELF accepted")
	}
	binary.LittleEndian.PutUint16(header[16:18], 1)
	if err := verifyArtifactELF(header, "arm64"); err == nil {
		t.Fatal("relocatable object accepted as executable")
	}
}
