// Package auth provides the primitives session and API-key authentication
// are built on: password hashing and high-entropy token generation. It
// holds no state and knows nothing about HTTP or storage — internal/api
// wires this together with internal/store. See SECURITY.md for the rules
// this exists to satisfy (hashed passwords, hashed API keys, no plaintext
// secrets at rest).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is raised above bcrypt.DefaultCost (10, set when hardware was
// slower) to keep the hash expensive to brute-force on modern hardware.
const bcryptCost = 12

// HashPassword returns a bcrypt hash suitable for storing in place of a
// plaintext password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches the given bcrypt hash.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is a valid bcrypt hash of an arbitrary, unused password, spent
// only to burn the same CPU time a real VerifyPassword call would — see
// VerifyPasswordConstantTime.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("doupro-timing-safety"), bcryptCost)

// VerifyPasswordConstantTime is VerifyPassword for the case where hash may
// be empty (e.g. the account wasn't found). Login must take the same time
// whether or not the username exists — comparing against req.Password
// only when a user is found lets an attacker distinguish "no such user"
// (fast) from "wrong password" (slow, bcrypt ran) by response time, which
// leaks which usernames exist. Running bcrypt against a fixed dummy hash
// in the not-found case closes that side channel; the result is always
// false, only the timing matters.
func VerifyPasswordConstantTime(hash, password string) bool {
	if hash == "" {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) //nolint:errcheck // timing-only, result discarded
		return false
	}
	return VerifyPassword(hash, password)
}

// GenerateToken returns a random, URL-safe hex token (32 bytes of entropy)
// suitable for session tokens and API keys.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns the SHA-256 hex digest of a token, for storing
// high-entropy tokens (sessions, API keys) without keeping the value
// itself at rest — unlike passwords these are already high-entropy, so a
// fast, constant-time-comparable hash is appropriate (bcrypt is not: it's
// designed to be slow, which matters for low-entropy secrets, not these).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokensEqual compares two hex-encoded hashes in constant time.
func TokensEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
