// Package auth hashes passwords and signs session cookies.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Default argon2id parameters (64 MiB, 3 passes, 2 lanes).
const (
	memory  = 64 * 1024
	passes  = 3
	threads = 2
	keyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns argon2id:<m>:<t>:<p>:<salt>:<hash>. The format has no
// "$" so it survives docker compose variable interpolation.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, passes, memory, threads, keyLen)
	return fmt.Sprintf("argon2id:%d:%d:%d:%s:%s", memory, passes, threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks pw against a hash from HashPassword in constant time.
func VerifyPassword(hash, pw string) bool {
	p := strings.Split(hash, ":")
	if len(p) != 6 || p[0] != "argon2id" {
		return false
	}
	m, e1 := strconv.ParseUint(p[1], 10, 32)
	t, e2 := strconv.ParseUint(p[2], 10, 32)
	th, e3 := strconv.ParseUint(p[3], 10, 8)
	salt, e4 := b64.DecodeString(p[4])
	want, e5 := b64.DecodeString(p[5])
	if errors.Join(e1, e2, e3, e4, e5) != nil || len(want) == 0 || m > 1<<22 || t > 64 || th == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, uint32(t), uint32(m), uint8(th), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// EqualString compares strings in constant time.
func EqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Signer signs and verifies session values.
type Signer struct{ Key []byte }

func (s Signer) mac(msg string) string {
	h := hmac.New(sha256.New, s.Key)
	h.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Issue returns a cookie value for user valid until exp.
func (s Signer) Issue(user string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(user)) + "." + strconv.FormatInt(exp.Unix(), 10)
	return payload + "." + s.mac(payload)
}

// Verify returns the user of a valid, unexpired cookie value.
func (s Signer) Verify(v string, now time.Time) (string, bool) {
	i := strings.LastIndexByte(v, '.')
	if i < 0 {
		return "", false
	}
	payload, sig := v[:i], v[i+1:]
	if !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return "", false
	}
	u, exp, ok := strings.Cut(payload, ".")
	if !ok {
		return "", false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() >= e {
		return "", false
	}
	user, err := base64.RawURLEncoding.DecodeString(u)
	if err != nil {
		return "", false
	}
	return string(user), true
}
