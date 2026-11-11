package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// MinPasswordLen is the minimum accepted password length.
const MinPasswordLen = 12

var ErrWeakPassword = errors.New("password too short")

// HashPassword returns an argon2id hash in PHC string format.
func HashPassword(pw string) (string, error) {
	if len([]rune(pw)) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	salt := RandomBytes(16)
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword verifies pw against a PHC argon2id hash in constant time.
func CheckPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyCheck spends the same time as a real check (prevents user enumeration by timing).
func DummyCheck(pw string) {
	argon2.IDKey([]byte(pw), make([]byte, 16), argonTime, argonMemory, argonThreads, argonKeyLen)
}

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// NewToken returns a random URL-safe token (256 bit).
func NewToken() string { return base64.RawURLEncoding.EncodeToString(RandomBytes(32)) }

// TokenHash is what the database stores for a token.
func TokenHash(tok string) string {
	s := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(s[:])
}

// RandomPassword returns a random password of n characters without look-alike characters.
func RandomPassword(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_"
	out := make([]byte, n)
	b := RandomBytes(n)
	for i := range out {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

// RecoveryCodes returns n codes like "abcd-efgh-jkmn".
func RecoveryCodes(n int) []string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	codes := make([]string, n)
	for i := range codes {
		b := RandomBytes(12)
		var sb strings.Builder
		for j := 0; j < 12; j++ {
			if j > 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(b[j])%len(alphabet)])
		}
		codes[i] = sb.String()
	}
	return codes
}

// NormalizeRecovery lowercases and strips spaces so "ABCD EFGH JKMN" matches.
func NormalizeRecovery(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	s = strings.ReplaceAll(s, "-", "")
	if len(s) == 12 {
		return s[0:4] + "-" + s[4:8] + "-" + s[8:12]
	}
	return s
}
