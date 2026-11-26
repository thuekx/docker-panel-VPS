package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLoginsAndAlerts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	rec := func(ip, res string, at time.Time) bool {
		n, err := s.RecordLogin(1, 7, ip, "Mozilla/5.0", res, at)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if rec("198.51.100.1", "ok", t0) { // the very first sign-in is not "new"
		t.Error("first sign-in new")
	}
	if rec("198.51.100.1", "ok", t0.Add(time.Hour)) || rec("203.0.113.9", "password", t0.Add(2*time.Hour)) {
		t.Error("same address or a failure counted new")
	}
	if !rec("203.0.113.9", "ok", t0.Add(3*time.Hour)) || rec("203.0.113.9", "recovery", t0.Add(4*time.Hour)) {
		t.Error("new address not seen, or seen twice")
	}
	l, _ := s.Logins(7, 10)
	if len(l) != 5 || l[0].Result != "recovery" || !l[1].NewIP || l[1].IP != "203.0.113.9" || l[4].NewIP {
		t.Fatalf("%+v", l)
	}
	if o, _ := s.Logins(8, 10); len(o) != 0 {
		t.Error("another user's logins")
	}
	// 91 days later the old attempts are gone; with no sign-in left to compare, nothing counts as new
	if rec("198.51.100.1", "ok", t0.Add(91*24*time.Hour+4*time.Hour)) {
		t.Error("new without anything to compare")
	}
	if l, _ := s.Logins(7, 10); len(l) != 1 {
		t.Errorf("old attempts kept: %d", len(l))
	}
	for i := 0; i < 205; i++ {
		_ = s.AddAlert(1, "disk", fmt.Sprint(i), "Telegram: OK", t0)
	}
	_ = s.AddAlert(2, "disk", "other", "", t0)
	a, _ := s.Alerts(1, 300)
	if len(a) != 200 || a[0].Text != "204" {
		t.Fatalf("%d %+v", len(a), a[0])
	}
	if b, _ := s.Alerts(2, 10); len(b) != 1 {
		t.Error("org 2 alerts dropped")
	}
}
