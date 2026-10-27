package auth

import (
	"strings"
	"testing"
)

func TestNewAPIKey(t *testing.T) {
	secret, prefix, hash, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, KeyPrefix) || len(secret) < 40 {
		t.Fatalf("bad secret %q", secret)
	}
	if !strings.HasPrefix(secret, prefix) {
		t.Fatalf("prefix %q not in secret", prefix)
	}
	if hash != HashSecret(secret) || len(hash) != 64 {
		t.Fatalf("bad hash")
	}
	if _, _, _, err := NewAPIKey(); err != nil {
		t.Fatal(err)
	}
}

func TestPasswords(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("expected short password error")
	}
	h, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct-horse-battery") {
		t.Fatal("password check failed")
	}
	if CheckPassword(h, "wrong") {
		t.Fatal("wrong password accepted")
	}
}
