package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clamHooks(t *testing.T, m *Manager, f *fakeRunner) {
	t.Helper()
	m.memFreeFn = func() int { return 4000 }
	found := exit1(t)
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, "--entrypoint freshclam"):
			_ = os.WriteFile(filepath.Join(m.clamDir(), "db", "daily.cvd"), []byte("x"), 0o644)
			return []byte("ok"), nil, true
		case strings.Contains(j, "--entrypoint clamscan"):
			return []byte(clamOut), found, true
		}
		return nil, nil, false
	}
}

func TestQuarantine(t *testing.T) {
	m, _, id := readySite(t)
	dir := filepath.Join(m.wpDir(id), "wp-content", "uploads")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "shell.php"), []byte("<?php evil();"), 0o640)
	q, err := m.QuarantineFile(id, "/wp-content/uploads/shell.php", "Php.Webshell.Generic-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shell.php")); err == nil {
		t.Fatal("the file is still in the website")
	}
	if q.Size != 13 || q.Mode != 0o640 || len(q.SHA256) != 64 || q.Domain != "voltix.id" || q.Path != "/wp-content/uploads/shell.php" {
		t.Fatalf("meta %+v", q)
	}
	if fi, _ := os.Stat(filepath.Join(m.quarantineDir(), q.ID, "file")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(m.quarantineDir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", fi.Mode())
	}
	list, _ := m.Quarantine()
	if len(list) != 1 || list[0].ID != q.ID {
		t.Fatalf("list %+v", list)
	}
	// refusals
	for _, p := range []string{"/../../etc/passwd", "/wp-content", "/nope.php"} {
		if _, err := m.QuarantineFile(id, p, "x", false); err == nil {
			t.Errorf("%s moved", p)
		}
	}
	for _, bad := range []string{"../x", "q123", "Q0123456789abcdef"} {
		if _, err := m.QuarantineRestore(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// a file at the old path blocks the restore
	os.WriteFile(filepath.Join(dir, "shell.php"), []byte("new"), 0o644)
	if _, err := m.QuarantineRestore(q.ID); !errors.Is(err, ErrBadInput) {
		t.Fatalf("restore over a file: %v", err)
	}
	os.Remove(filepath.Join(dir, "shell.php"))
	if _, err := m.QuarantineRestore(q.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "shell.php")); string(b) != "<?php evil();" {
		t.Fatalf("restored %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "shell.php")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode %v", fi.Mode())
	}
	if list, _ := m.Quarantine(); len(list) != 0 {
		t.Fatal("still listed after restore")
	}
	// delete for good
	q, _ = m.QuarantineFile(id, "/wp-content/uploads/shell.php", "x", false)
	if _, err := m.QuarantineDelete(q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.quarantineDir(), q.ID)); err == nil {
		t.Fatal("not removed")
	}
}

func TestScanSchedule(t *testing.T) {
	m, f, id := readySite(t)
	clamHooks(t, m, f)
	if _, err := m.SetScanSchedule(id, "hourly", false); !errors.Is(err, ErrBadInput) {
		t.Fatalf("bad schedule: %v", err)
	}
	if _, err := m.SetScanSchedule(id, "daily", true); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(id); s.ScanEvery == nil || s.ScanEvery.Every != "daily" || !s.ScanEvery.Quarantine {
		t.Fatalf("site %+v", s.ScanEvery)
	}
	dir := filepath.Join(m.wpDir(id), "wp-content", "uploads")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "shell.php"), []byte("<?php"), 0o644)
	ctx := context.Background()
	// daytime: nothing runs
	m.now = func() time.Time { return time.Date(2026, 11, 26, 14, 0, 0, 0, wib) }
	m.ScanTick(ctx)
	if m.readScan(id) != nil {
		t.Fatal("scanned in the daytime")
	}
	// night: the due website is scanned, the found file is quarantined, an event is added
	m.now = func() time.Time { return time.Date(2026, 11, 27, 3, 0, 0, 0, wib) }
	m.ScanTick(ctx)
	r := m.readScan(id)
	if r == nil || !r.Scheduled || len(r.Found) != 2 || !r.Found[0].Quarantined || r.Found[1].Quarantined {
		t.Fatalf("result %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "shell.php")); err == nil {
		t.Fatal("found file left in place")
	}
	if list, _ := m.Quarantine(); len(list) != 1 || !list[0].Auto {
		t.Fatalf("quarantine %+v", list)
	}
	ev := m.Watchdog(0).Events
	if len(ev) != 1 || ev[0].Service != "malware" || ev[0].Kind != "found" || ev[0].Site != id || ev[0].Reason != "2 quarantined" {
		t.Fatalf("events %+v", ev)
	}
	// not due again the same night
	n := len(f.calls)
	m.now = func() time.Time { return time.Date(2026, 11, 27, 5, 0, 0, 0, wib) }
	m.ScanTick(ctx)
	if len(f.calls) != n {
		t.Fatal("scanned again before it was due")
	}
	// too little memory: waits
	m.now = func() time.Time { return time.Date(2026, 11, 28, 3, 0, 0, 0, wib) }
	m.memFreeFn = func() int { return 500 }
	m.ScanTick(ctx)
	if len(f.calls) != n {
		t.Fatal("scanned without memory")
	}
	if _, err := m.SetScanSchedule(id, "", false); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(id); s.ScanEvery != nil {
		t.Fatal("schedule not cleared")
	}
}
