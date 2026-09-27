package agent

import (
	"bytes"
	"debug/elf"
	"errors"
)

func verifyArtifactELF(binary []byte, architecture string) error {
	value, err := elf.NewFile(bytes.NewReader(binary))
	if err != nil {
		return errors.New("agent: native artifact is not a valid ELF executable")
	}
	defer value.Close()
	expected := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[architecture]
	if expected == elf.EM_NONE || value.Machine != expected || value.Class != elf.ELFCLASS64 || value.Data != elf.ELFDATA2LSB || (value.Type != elf.ET_EXEC && value.Type != elf.ET_DYN) {
		return errors.New("agent: native artifact platform does not match its declaration")
	}
	return nil
}
