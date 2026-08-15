// Package secret provides small, audited primitives for at-rest configuration
// encryption. It deliberately does not manage passwords or transport keys.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// KeySize is the AES-256 key length in bytes.
	KeySize = 32
)

var (
	ErrInvalidKey = errors.New("secret: key must be 32 bytes")
	ErrUnsafeMode = errors.New("secret: key file permissions are too broad")
)

// NewKey creates an AES-256 key from the operating system random source.
func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("secret: generate key: %w", err)
	}
	return key, nil
}

// Seal returns nonce || ciphertext. The nonce is randomly generated for every
// value and is safe to store alongside the ciphertext.
func Seal(key, plaintext, additionalData []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secret: generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, additionalData), nil
}

// Open decrypts a value created by Seal.
func Open(key, sealed, additionalData []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: create gcm: %w", err)
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("secret: ciphertext is too short")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt: %w", err)
	}
	return plaintext, nil
}

// LoadOrCreateKey creates a 0600 key file, or verifies and reads an existing
// one. It refuses an existing key readable by group or other users.
func LoadOrCreateKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secret: create key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		key, keyErr := NewKey()
		if keyErr != nil {
			_ = file.Close()
			return nil, keyErr
		}
		if _, writeErr := file.Write(key); writeErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("secret: write key: %w", writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("secret: close key: %w", closeErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("secret: open key: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("secret: stat key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnsafeMode
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secret: read key: %w", err)
	}
	if len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	return key, nil
}
