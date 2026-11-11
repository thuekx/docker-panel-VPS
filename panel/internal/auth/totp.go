// Package auth holds password hashing, TOTP and token helpers.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit base32 secret.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// TOTPCode computes the RFC 6238 code (SHA1, 30 s, 6 digits) for a counter.
func totpAt(secret string, counter uint64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000), nil
}

// TOTPCode returns the code for time t.
func TOTPCode(secret string, t time.Time) (string, error) {
	return totpAt(secret, uint64(t.Unix()/30))
}

// VerifyTOTP accepts the code for t and one step before/after. It returns the matched step
// so callers can refuse replays of the same step.
func VerifyTOTP(secret, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	step := t.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		c, err := totpAt(secret, uint64(step+d))
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			return step + d, true
		}
	}
	return 0, false
}

// OTPAuthURL builds the URI for authenticator apps.
func OTPAuthURL(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}
