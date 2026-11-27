package authlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth-fail.log")
	l := Open(p, "panel")
	l.now = func() time.Time { return time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC) }
	for _, ip := range []string{"203.0.113.9", "::ffff:198.51.100.7", "2001:db8::5", "not an ip", "1.2.3.4 service=x\nfake", ""} {
		l.Fail(ip)
	}
	b, _ := os.ReadFile(p)
	want := "2026-11-26T15:00:00Z nostek-auth fail ip=203.0.113.9 service=panel\n" +
		"2026-11-26T15:00:00Z nostek-auth fail ip=198.51.100.7 service=panel\n" +
		"2026-11-26T15:00:00Z nostek-auth fail ip=2001:db8::5 service=panel\n"
	if string(b) != want {
		t.Fatalf("log:\n%s", b)
	}
	os.WriteFile(p, []byte(strings.Repeat("x", MaxSize+1)), 0o644)
	l.Fail("203.0.113.9")
	if fi, _ := os.Stat(p); fi.Size() > 100 {
		t.Fatalf("not emptied: %d", fi.Size())
	}
	var nl *Log
	nl.Fail("203.0.113.9")
	Open("", "panel").Fail("203.0.113.9")
}
