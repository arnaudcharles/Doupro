// Package secrets encrypts reversible application secrets before SQLite
// persistence. Passwords, sessions and API keys remain one-way hashes.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "enc:v1:"

type Cipher struct{ aead cipher.AEAD }

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func LoadOrCreate(encoded, path string) (*Cipher, error) {
	var key []byte
	var err error
	if encoded != "" {
		key, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode DOUPRO_ENCRYPTION_KEY: %w", err)
		}
	} else {
		key, err = loadOrCreateFile(path)
		if err != nil {
			return nil, err
		}
	}
	return New(key)
}

func loadOrCreateFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("encryption key file path is empty")
	}
	if data, err := os.ReadFile(path); err == nil {
		key, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decErr != nil {
			return nil, fmt.Errorf("decode encryption key file %s: %w", path, decErr)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read encryption key file %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create encryption key directory: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	data := []byte(base64.StdEncoding.EncodeToString(key) + "\n")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadOrCreateFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create encryption key file %s: %w", path, err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, fmt.Errorf("write encryption key file: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close encryption key file: %w", closeErr)
	}
	return key, nil
}

func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if IsEncrypted(plaintext) {
		return plaintext, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate encryption nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return "", errors.New("encrypted secret is truncated")
	}
	plain, err := c.aead.Open(nil, raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("decrypt secret: wrong key or corrupted ciphertext")
	}
	return string(plain), nil
}

func IsEncrypted(value string) bool { return strings.HasPrefix(value, prefix) }
