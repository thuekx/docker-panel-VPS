package auth

import (
	"testing"
	"time"
)

// RFC 6238 appendix B test vector (SHA1, secret "12345678901234567890").
func TestTOTPVector(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"}
	for ts, want := range cases {
		got, err := TOTPCode(secret, time.Unix(ts, 0))
		if err != nil || got != want {
			t.Fatalf("t=%d got %s want %s (%v)", ts, got, want, err)
		}
	}
}

func TestTOTPWindow(t *testing.T) {
	s := NewTOTPSecret()
	now := time.Unix(1700000000, 0)
	c, _ := TOTPCode(s, now.Add(-30*time.Second))
	if _, ok := VerifyTOTP(s, c, now); !ok {
		t.Fatal("previous step should pass")
	}
	c, _ = TOTPCode(s, now.Add(-90*time.Second))
	if _, ok := VerifyTOTP(s, c, now); ok {
		t.Fatal("old step must fail")
	}
}

func TestPassword(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrWeakPassword {
		t.Fatal("short password accepted")
	}
	h, err := HashPassword("correct horse battery")
	if err != nil || !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong horse battery") {
		t.Fatal("hash/check broken")
	}
}

func TestSeal(t *testing.T) {
	tok := NewToken()
	b, _ := Seal(tok, []byte("secret"))
	if p, err := Open(tok, b); err != nil || string(p) != "secret" {
		t.Fatal("open failed")
	}
	if _, err := Open(NewToken(), b); err == nil {
		t.Fatal("wrong token opened the seal")
	}
}

func TestRecoveryFormat(t *testing.T) {
	for _, c := range RecoveryCodes(10) {
		if NormalizeRecovery(c) != c || NormalizeRecovery(" "+toUpper(c)+" ") != c {
			t.Fatalf("normalize %q", c)
		}
	}
}

func toUpper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatal("blocked early")
		}
	}
	if l.Allow("k") {
		t.Fatal("not blocked")
	}
	if !l.Allow("other") {
		t.Fatal("keys not separate")
	}
}
