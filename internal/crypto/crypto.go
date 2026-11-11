// Package crypto handles secrets at rest, password hashing and random tokens.
//
//	MASTER_KEY (Docker secret) + salt (per install, next to the database)
//	    │ Argon2id (64 MiB, 4 passes): each guess of a stolen copy costs that
//	    ▼
//	master ──HKDF(purpose)──► database file key (Adiantum)
//	                     ├──► AES-256-GCM key ──► nonce(12) ‖ ciphertext‖tag  in *_enc
//	                     └──► HMAC key (webhook URLs)
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	nonceLen   = 12
	keyLen     = 32
	tokenBytes = 32

	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32

	// The master key is derived once per start, so it may cost more
	// than a login.
	masterTime = 4
	saltLen    = 16
)

// Purpose scopes a derived key to one use, so a key never crosses purposes.
type Purpose string

const (
	PurposeCredential Purpose = "credential"
	PurposeTOTP       Purpose = "totp"
	PurposeNotify     Purpose = "notify"
	PurposeSetting    Purpose = "setting"
	PurposeHook       Purpose = "hook"
	PurposeDatabase   Purpose = "database"
)

// ErrMissingKey means crypto was used before Init or without a master key.
var ErrMissingKey = errors.New("crypto: master key not initialised")

var master []byte

// Init sets the process-wide master key (see Derive).
func Init(m []byte) {
	master = m
}

var (
	derivedMu sync.Mutex
	derived   = map[string][]byte{} // secret and salt → master, one Argon2id run each
)

// Derive turns MASTER_KEY and the install's salt into the master key.
// Argon2id makes every guess against a stolen database cost 64 MiB and
// several passes, where a hash would allow billions a second.
func Derive(secret string, salt []byte) []byte {
	id := secret + "\x00" + string(salt)
	derivedMu.Lock()
	defer derivedMu.Unlock()
	if m, ok := derived[id]; ok {
		return m
	}
	m := argon2.IDKey([]byte(secret), salt, masterTime, argonMemory, argonThreads, keyLen)
	derived[id] = m
	return m
}

// Legacy is the master key of databases from before salts: SHA-256 of
// MASTER_KEY. Only to open them once and re-encrypt under Derive.
func Legacy(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// NewSalt returns a fresh random salt for Derive.
func NewSalt() []byte {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	return salt
}

// A strong master key is long and varied, like `openssl rand -base64 32`.
const (
	minKeyLen   = 32
	minKeyChars = 16 // distinct characters
)

// WeakKey reports whether a master key could be guessed offline: Derive
// slows each guess, but a short or repetitive key is in every wordlist.
func WeakKey(secret string) bool {
	distinct := map[rune]bool{}
	for _, r := range secret {
		distinct[r] = true
	}
	return len(secret) < minKeyLen || len(distinct) < minKeyChars
}

func key(purpose Purpose, m []byte) ([]byte, error) {
	if m == nil {
		m = master
	}
	if m == nil {
		return nil, ErrMissingKey
	}
	out := make([]byte, keyLen)
	if _, err := hkdf.New(sha256.New, m, nil, []byte(purpose)).Read(out); err != nil {
		return nil, err
	}
	return out, nil
}

// DatabaseKey is the database file key, under the process master key or
// an explicit one (key rotation).
func DatabaseKey(m []byte) ([]byte, error) {
	return key(PurposeDatabase, m)
}

// Encrypt seals text under purpose, optionally with an explicit master key
// (used only for offline tools such as key rotation).
func Encrypt(text string, purpose Purpose, m []byte) ([]byte, error) {
	k, err := key(purpose, m)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, []byte(text), []byte(purpose))
	return append(nonce, ct...), nil
}

// Reseal re-encrypts a blob from the process master key to master to
// (key rotation).
func Reseal(blob []byte, purpose Purpose, to []byte) ([]byte, error) {
	text, err := Decrypt(blob, purpose)
	if err != nil {
		return nil, err
	}
	return Encrypt(text, purpose, to)
}

// Decrypt opens a blob sealed by Encrypt under the process master key.
func Decrypt(blob []byte, purpose Purpose) (string, error) {
	if len(blob) < nonceLen {
		return "", errors.New("crypto: ciphertext too short")
	}
	k, err := key(purpose, nil)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce, body := blob[:nonceLen], blob[nonceLen:]
	pt, err := gcm.Open(nil, nonce, body, []byte(purpose))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// HashPassword returns a self-describing argon2id hash (PHC-like string).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s",
		argonTime, argonMemory, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// CheckPassword verifies a password against a hash from HashPassword. A
// missing stored hash still spends comparable time, so missing accounts are
// not detectable by timing.
func CheckPassword(stored, password string) bool {
	if stored == "" {
		_, _ = HashPassword(password)
		return false
	}

	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		_, _ = HashPassword(password)
		return false
	}
	t, err1 := strconv.Atoi(parts[1])
	mem, err2 := strconv.Atoi(parts[2])
	threads, err3 := strconv.Atoi(parts[3])
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return false
	}

	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(mem), uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns a random URL-safe token for sessions, invites and resets.
func NewToken() string {
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenHash stores tokens hashed: a database leak does not leak sessions.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

// Same does a constant-time string comparison (CSRF tokens, etc.).
func Same(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// Sign returns a URL-safe HMAC of message under purpose, e.g. the secret
// part of an inbound webhook URL.
func Sign(message string, purpose Purpose) (string, error) {
	k, err := key(purpose, nil)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// MaskToken hides a token behind a fresh random pad, so it never repeats
// in compressed pages (BREACH):
//
//	base64url(pad ‖ pad XOR token), pad random, len(pad) = len(token)
func MaskToken(token string) string {
	pad := make([]byte, len(token))
	if _, err := rand.Read(pad); err != nil {
		panic(err) // the OS random source never fails on supported systems
	}
	out := make([]byte, 2*len(token))
	copy(out, pad)
	for i := range len(token) {
		out[len(token)+i] = pad[i] ^ token[i]
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

// UnmaskToken reverses MaskToken; false for anything it did not make.
func UnmaskToken(masked string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(masked)
	if err != nil || len(raw) == 0 || len(raw)%2 != 0 {
		return "", false
	}
	half := len(raw) / 2
	out := make([]byte, half)
	for i := range half {
		out[i] = raw[i] ^ raw[half+i]
	}
	return string(out), true
}
