package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "$") || !strings.HasPrefix(h, "argon2id:65536:3:2:") {
		t.Fatalf("format %q", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "wrong") || VerifyPassword("bogus", "x") {
		t.Fatal("verify")
	}
}

func TestSigner(t *testing.T) {
	s := Signer{Key: []byte(strings.Repeat("k", 32))}
	now := time.Unix(1000, 0)
	v := s.Issue("admin", now.Add(time.Hour))
	if u, ok := s.Verify(v, now); !ok || u != "admin" {
		t.Fatal("valid cookie rejected")
	}
	if _, ok := s.Verify(v, now.Add(2*time.Hour)); ok {
		t.Fatal("expired cookie accepted")
	}
	if _, ok := s.Verify(v+"x", now); ok {
		t.Fatal("tampered cookie accepted")
	}
	other := Signer{Key: []byte(strings.Repeat("o", 32))}
	if _, ok := other.Verify(v, now); ok {
		t.Fatal("cookie from another key accepted")
	}
}
