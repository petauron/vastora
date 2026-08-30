// Package controlplane defines the cryptographic wire contract shared by
// Center and Agent. TLS authenticates Center; a per-Agent X25519 key encrypts
// every leased task so task secrets are not exposed to intermediaries or logs.
package controlplane

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	KeySize         = 32
	MaxJSONPayload  = 1 << 20
	MaxTaskPayload  = 4 << 20
	MaxEnvelopeWire = 6 << 20
	envelopeVersion = 1
)

var x25519 = ecdh.X25519()

type Envelope struct {
	Version            int    `json:"version"`
	EphemeralPublicKey []byte `json:"ephemeralPublicKey"`
	Nonce              []byte `json:"nonce"`
	Ciphertext         []byte `json:"ciphertext"`
}

func GenerateKeyPair() (privateKey, publicKey []byte, err error) {
	private, err := x25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("control plane: generate X25519 key: %w", err)
	}
	return private.Bytes(), private.PublicKey().Bytes(), nil
}

func PublicKey(privateKey []byte) ([]byte, error) {
	private, err := x25519.NewPrivateKey(privateKey)
	if err != nil {
		return nil, errors.New("control plane: invalid X25519 private key")
	}
	return private.PublicKey().Bytes(), nil
}

func ValidatePublicKey(publicKey []byte) error {
	if len(publicKey) != KeySize {
		return errors.New("control plane: invalid X25519 public key")
	}
	if _, err := x25519.NewPublicKey(publicKey); err != nil {
		return errors.New("control plane: invalid X25519 public key")
	}
	return nil
}

func Seal(publicKey, plaintext, additionalData []byte) (Envelope, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxTaskPayload {
		return Envelope{}, errors.New("control plane: task payload exceeds the allowed size")
	}
	peer, err := x25519.NewPublicKey(publicKey)
	if err != nil {
		return Envelope{}, errors.New("control plane: invalid Agent X25519 public key")
	}
	ephemeral, err := x25519.GenerateKey(rand.Reader)
	if err != nil {
		return Envelope{}, fmt.Errorf("control plane: generate ephemeral X25519 key: %w", err)
	}
	shared, err := ephemeral.ECDH(peer)
	if err != nil {
		return Envelope{}, errors.New("control plane: derive task encryption key")
	}
	aead, err := taskAEAD(shared, ephemeral.PublicKey().Bytes(), additionalData)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("control plane: generate task nonce: %w", err)
	}
	return Envelope{
		Version:            envelopeVersion,
		EphemeralPublicKey: ephemeral.PublicKey().Bytes(),
		Nonce:              nonce,
		Ciphertext:         aead.Seal(nil, nonce, plaintext, additionalData),
	}, nil
}

func Open(privateKey []byte, envelope Envelope, additionalData []byte) ([]byte, error) {
	if envelope.Version != envelopeVersion || len(envelope.EphemeralPublicKey) != KeySize || len(envelope.Ciphertext) == 0 || len(envelope.Ciphertext) > MaxTaskPayload+64 {
		return nil, errors.New("control plane: invalid encrypted task envelope")
	}
	private, err := x25519.NewPrivateKey(privateKey)
	if err != nil {
		return nil, errors.New("control plane: invalid Agent X25519 private key")
	}
	peer, err := x25519.NewPublicKey(envelope.EphemeralPublicKey)
	if err != nil {
		return nil, errors.New("control plane: invalid ephemeral X25519 public key")
	}
	shared, err := private.ECDH(peer)
	if err != nil {
		return nil, errors.New("control plane: derive task decryption key")
	}
	aead, err := taskAEAD(shared, envelope.EphemeralPublicKey, additionalData)
	if err != nil {
		return nil, err
	}
	if len(envelope.Nonce) != aead.NonceSize() {
		return nil, errors.New("control plane: invalid encrypted task nonce")
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, additionalData)
	if err != nil {
		return nil, errors.New("control plane: encrypted task authentication failed")
	}
	if len(plaintext) == 0 || len(plaintext) > MaxTaskPayload {
		return nil, errors.New("control plane: decrypted task payload exceeds the allowed size")
	}
	return plaintext, nil
}

func TaskAdditionalData(agentID, taskID string, attempt int64) []byte {
	digest := sha256.New()
	digest.Write([]byte("vastora-control-plane-task-v1\x00"))
	digest.Write([]byte(agentID))
	digest.Write([]byte{0})
	digest.Write([]byte(taskID))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(attempt))
	digest.Write(encoded[:])
	return digest.Sum(nil)
}

func taskAEAD(shared, ephemeralPublicKey, additionalData []byte) (cipher.AEAD, error) {
	reader := hkdf.New(sha256.New, shared, ephemeralPublicKey, append([]byte("vastora-control-plane-envelope-v1\x00"), additionalData...))
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("control plane: derive task encryption key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("control plane: initialize task encryption: %w", err)
	}
	return cipher.NewGCM(block)
}
