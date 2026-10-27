package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const KeyPrefix = "ocgr_"

// NewAPIKey returns a new secret, its visible prefix and its storage hash.
func NewAPIKey() (secret, prefix, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	secret = KeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	prefix = secret[:min(len(secret), 13)]
	hash = HashSecret(secret)
	return secret, prefix, hash, nil
}

// HashSecret hashes a high-entropy secret with SHA-256. API keys are random
// 256-bit values, so a fast hash is appropriate; admin passwords use bcrypt.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func NewSessionToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashSecret(token), nil
}

func NewRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b)
}

// NewID returns a random identifier with a short prefix.
func NewID(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return prefix + "unknown"
	}
	return prefix + hex.EncodeToString(b)
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func ValidatePassword(password string) error {
	if len(password) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	if len(password) > 200 {
		return fmt.Errorf("password must be at most 200 characters")
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password must not be blank")
	}
	return nil
}

// GeneratePassword creates a strong one-time password for first boot.
func GeneratePassword() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "change-me-now-please"
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
