// Package dantebundle carries the native landing server inside released Agents.
// Source-only Go builds intentionally cannot provision a landing server.
package dantebundle

import (
	"bytes"
	"compress/gzip"
	"debug/elf"
	"embed"
	"errors"
	"io"
	"runtime"
)

//go:embed assets/*
var assets embed.FS

const MaxBinarySize = 16 << 20

func Read() (binary, license, notice []byte, err error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, nil, nil, errors.New("agent: unsupported landing platform")
	}
	compressed, err := assets.ReadFile("assets/danted-linux-" + runtime.GOARCH + ".gz")
	if err != nil {
		return nil, nil, nil, errors.New("agent: this Agent build does not include the landing server")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, nil, nil, err
	}
	defer reader.Close()
	binary, err = io.ReadAll(io.LimitReader(reader, MaxBinarySize+1))
	if err != nil || len(binary) > MaxBinarySize {
		return nil, nil, nil, errors.New("agent: invalid bundled landing executable")
	}
	image, err := elf.NewFile(bytes.NewReader(binary))
	if err != nil {
		return nil, nil, nil, errors.New("agent: invalid bundled landing executable")
	}
	want := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		want = elf.EM_AARCH64
	}
	if image.Class != elf.ELFCLASS64 || image.Machine != want {
		return nil, nil, nil, errors.New("agent: bundled landing architecture does not match")
	}
	license, err = assets.ReadFile("assets/Dante-LICENSE")
	if err != nil || len(license) == 0 {
		return nil, nil, nil, errors.New("agent: bundled landing license is missing")
	}
	notice, err = assets.ReadFile("assets/Dante-NOTICE")
	if err != nil || len(notice) == 0 {
		return nil, nil, nil, errors.New("agent: bundled landing notice is missing")
	}
	return binary, license, notice, nil
}
