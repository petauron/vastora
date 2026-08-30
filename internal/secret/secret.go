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

const databaseKeyBindingPlaintext = "vastora-database-key-binding-v1"

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

// LoadKey verifies and reads an existing key. It refuses a key readable by
// group or other users.
func LoadKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("secret: stat key: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret: key is not a regular file")
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

// CreateKey creates a new 0600 key file and never replaces an existing path.
func CreateKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secret: create key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("secret: create key: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(key); err != nil {
		return nil, fmt.Errorf("secret: write key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("secret: sync key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("secret: close key: %w", err)
	}
	committed = true
	return key, nil
}

// LoadOrCreateKey is retained for callers which do not own a database
// lifecycle. Database stores must inspect their database before creating a key.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := LoadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return CreateKey(path)
}

// SealDatabaseKeyBinding creates the authenticated sentinel stored beside a
// database. The component name gives Center and Agent distinct AAD domains.
func SealDatabaseKeyBinding(key []byte, component string) ([]byte, error) {
	return Seal(key, []byte(databaseKeyBindingPlaintext), []byte("database-key-binding:"+component+":v1"))
}

// VerifyDatabaseKeyBinding verifies an authenticated database sentinel.
func VerifyDatabaseKeyBinding(key, sealed []byte, component string) error {
	plaintext, err := Open(key, sealed, []byte("database-key-binding:"+component+":v1"))
	if err != nil {
		return fmt.Errorf("secret: database key binding does not match: %w", err)
	}
	if string(plaintext) != databaseKeyBindingPlaintext {
		return errors.New("secret: database key binding is invalid")
	}
	return nil
}
