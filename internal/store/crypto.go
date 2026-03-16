package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

const (
	aesKeyLen   = 32 // AES-256
	gcmNonceLen = 12 // standard GCM nonce length
)

// parseEncryptionKey decodes a hex-encoded 32-byte AES-256 key.
// Returns an error if hexKey is empty, not valid hex, or not exactly 32 bytes.
func parseEncryptionKey(hexKey string) ([]byte, error) {
	if hexKey == "" {
		return nil, ErrEncryptionKeyNotConfigured
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decoding encryption key (must be hex-encoded): %w", err)
	}
	if len(key) != aesKeyLen {
		return nil, fmt.Errorf(
			"encryption key must be %d bytes (%d hex chars), got %d bytes",
			aesKeyLen, aesKeyLen*2, len(key),
		)
	}
	return key, nil
}

// encrypt encrypts plaintext using AES-256-GCM with a randomly generated nonce.
// Returns base64(nonce || ciphertext). Each call produces a unique ciphertext
// even for identical plaintexts (different nonces).
func encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonce := make([]byte, gcmNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	// Seal appends the ciphertext + tag after nonce.
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decrypts a base64-encoded nonce||ciphertext produced by encrypt.
// Returns an error if the data is malformed or the authentication tag is invalid
// (which occurs when the wrong key is used).
func decrypt(key []byte, cipherdata string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherdata)
	if err != nil {
		return "", fmt.Errorf("base64-decoding secret value: %w", err)
	}
	if len(raw) < gcmNonceLen {
		return "", fmt.Errorf("secret value too short to contain a nonce (len=%d)", len(raw))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating GCM wrapper: %w", err)
	}

	nonce, ciphertext := raw[:gcmNonceLen], raw[gcmNonceLen:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM authentication failure — wrong key or corrupted data.
		return "", fmt.Errorf("decrypting secret (wrong key or corrupted data): %w", err)
	}
	return string(plaintext), nil
}
