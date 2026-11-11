// Adapted from andon internal/crypto/crypto_test.go (468474a).

package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	Init(Derive("test-master-key", nil))
	blob, err := Encrypt("hunter2", PurposeSecret, nil)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := Decrypt(blob, PurposeSecret)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("want hunter2, got %q", got)
	}
}

func TestDecryptWrongPurposeFails(t *testing.T) {
	Init(Derive("test-master-key", nil))
	blob, _ := Encrypt("secret", PurposeSecret, nil)
	if _, err := Decrypt(blob, PurposePing); err == nil {
		t.Fatal("expected decrypt under wrong purpose to fail")
	}
}

func TestNewTokenUnique(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b {
		t.Fatal("tokens must be random")
	}
	if TokenHash(a) == TokenHash(b) {
		t.Fatal("hashes must differ for different tokens")
	}
}

// TestMaskToken: a masked token differs on every call (compressed pages
// must not repeat it, BREACH) and unmasks to the token; tampering fails.
func TestMaskToken(t *testing.T) {
	const token = "csrf-token-value"
	a, b := MaskToken(token), MaskToken(token)
	if a == b || strings.Contains(a, token) {
		t.Fatalf("masks repeat or leak: %q %q", a, b)
	}
	for _, masked := range []string{a, b} {
		if got, ok := UnmaskToken(masked); !ok || got != token {
			t.Fatalf("unmask %q: %q %v", masked, got, ok)
		}
	}
	if _, ok := UnmaskToken(a[:len(a)-2]); ok {
		t.Fatal("truncated token unmasked")
	}
	if _, ok := UnmaskToken("not base64 !"); ok {
		t.Fatal("garbage unmasked")
	}
}

// A master key is hashed without a work factor, so only a random key
// resists offline guessing: short or repetitive keys count as weak.
func TestWeakKey(t *testing.T) {
	for key, want := range map[string]bool{
		"":                                 true,
		"hunter2":                          true,
		"new-master-key":                   true,
		strings.Repeat("ab", 30):           true,
		"k3Jx9QpLm2VbN7wRt5YzA1sDf8GhUe4C": false, // 32 random characters
		"q1dW3V0r3a9mGx6+Yt7n2ZkQv5LbHs8PjR4uE0cXf1o=": false, // openssl rand -base64 32
	} {
		if got := WeakKey(key); got != want {
			t.Errorf("WeakKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// Derive is stable for a key and salt, differs per salt (one stolen
// install says nothing about another).
func TestDerive(t *testing.T) {
	a, b := []byte("salt-one-16bytes"), []byte("salt-two-16bytes")
	first := Derive("k3Jx9QpLm2VbN7wRt5YzA1sDf8GhUe4C", a)
	if !bytes.Equal(first, Derive("k3Jx9QpLm2VbN7wRt5YzA1sDf8GhUe4C", a)) || len(first) != keyLen {
		t.Fatal("not stable")
	}
	if bytes.Equal(first, Derive("k3Jx9QpLm2VbN7wRt5YzA1sDf8GhUe4C", b)) {
		t.Fatal("salt ignored")
	}
	if s1, s2 := NewSalt(), NewSalt(); len(s1) != saltLen || bytes.Equal(s1, s2) {
		t.Fatal("salts not random")
	}
}

func TestReady(t *testing.T) {
	Init(nil)
	if Ready() {
		t.Fatal("ready without a key")
	}
	if _, err := Encrypt("x", PurposeSecret, nil); err != ErrMissingKey {
		t.Fatalf("err = %v", err)
	}
	Init(Derive("test-master-key", nil))
	if !Ready() {
		t.Fatal("not ready with a key")
	}
}
