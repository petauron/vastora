// Package backupcrypto implements the existing Vastora encrypted backup envelope.
// Center's VASTORA1/version-2 format and key derivation remain unchanged.
package backupcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	Magic    = "VASTORA1"
	Version  = byte(2)
	saltSize = 16
)

func Encrypt(plain []byte, password string) ([]byte, error) {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("backup: create backup salt: %w", err)
	}
	key, err := deriveBackupKey(password, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, seal.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("backup: create backup nonce: %w", err)
	}
	header := append(append([]byte(Magic), Version), salt...)
	sealed := seal.Seal(nil, nonce, plain, header)
	return append(append(header, nonce...), sealed...), nil
}

func Decrypt(raw []byte, password string) ([]byte, error) {
	minimum := len(Magic) + 1 + saltSize + 12 + 16
	if len(raw) < minimum || string(raw[:len(Magic)]) != Magic || raw[len(Magic)] != Version {
		return nil, errors.New("backup: backup format is not supported")
	}
	headerEnd := len(Magic) + 1 + saltSize
	key, err := deriveBackupKey(password, raw[len(Magic)+1:headerEnd])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceEnd := headerEnd + seal.NonceSize()
	if len(raw) < nonceEnd+seal.Overhead() {
		return nil, errors.New("backup: backup is truncated")
	}
	plain, err := seal.Open(nil, raw[headerEnd:nonceEnd], raw[nonceEnd:], raw[:headerEnd])
	if err != nil {
		return nil, errors.New("backup: backup password or integrity check failed")
	}
	return plain, nil
}

func deriveBackupKey(password string, salt []byte) ([]byte, error) {
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("backup: derive backup key: %w", err)
	}
	return key, nil
}
