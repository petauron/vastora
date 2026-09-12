package agent

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

// VerifyOfficialNativeArtifact checks release assets without installing or
// executing them. It reuses the typed executor's download and archive rules.
func VerifyOfficialNativeArtifact(ctx context.Context, app catalog.AppManifest, artifact catalog.Artifact) error {
	if err := ValidateOfficialContract(app); err != nil {
		return err
	}
	declared := false
	for _, candidate := range app.Artifacts {
		if candidate == artifact {
			declared = true
		}
	}
	if !declared {
		return errors.New("agent: undeclared native artifact")
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("agent: unsafe artifact redirect")
		}
		return nil
	}}
	binary, err := (SystemdHostApplicationManager{HTTPClient: client}).downloadArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	if app.ID == "pulse-agent" {
		binary, err = pulseArchiveBinary(binary, app.Version, artifact.Architecture)
		if err != nil {
			return err
		}
	} else if app.ID != "komari-agent" {
		return errors.New("agent: unsupported native artifact contract")
	}
	return verifyArtifactELF(binary, artifact.Architecture)
}

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
