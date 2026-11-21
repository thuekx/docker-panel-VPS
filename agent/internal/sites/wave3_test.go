package sites

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const clamOut = `/scan/wp-content/uploads/shell.php: Php.Webshell.Generic-1 FOUND
/scan/x y/evil.js: Js.Trojan.Agent-2 FOUND

----------- SCAN SUMMARY -----------
Known viruses: 3627845
Engine version: 1.5.5
Scanned directories: 120
Scanned files: 1543
Infected files: 2
`

func TestParseScan(t *testing.T) {
	r := ScanResult{Found: []ScanFound{}}
	parseScan(clamOut, &r)
	if r.Files != 1543 || len(r.Found) != 2 || r.Found[0].Path != "/wp-content/uploads/shell.php" || r.Found[1].Path != "/x y/evil.js" ||
		r.Found[1].Signature != "Js.Trojan.Agent-2" || r.Signatures != "3627845 signatures, ClamAV 1.5.5" {
		t.Fatalf("%+v", r)
	}
}

// exit1 is an error with exit code 1, the way docker run reports clamscan finding something.
func exit1(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("no error")
	}
	return err
}

func TestMalwareScan(t *testing.T) {
	m, f, id := readySite(t)
	m.memFreeFn = func() int { return 4000 }
	found := exit1(t)
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, "--entrypoint freshclam"):
			_ = os.WriteFile(filepath.Join(m.clamDir(), "db", "daily.cvd"), []byte("x"), 0o644)
			old := time.Now().Add(-30 * 24 * time.Hour) // the file's own date is the release, not the download
			_ = os.Chtimes(filepath.Join(m.clamDir(), "db", "daily.cvd"), old, old)
			return []byte("daily.cvd updated"), nil, true
		case strings.Contains(j, "--entrypoint clamscan"):
			return []byte(clamOut), found, true
		}
		return nil, nil, false
	}
	job, err := m.StartScan(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	v, err := m.Security(id)
	if err != nil || v.Scan.Last == nil || len(v.Scan.Last.Found) != 2 || v.Scan.Job == nil || v.Scan.Last.Finished < v.Scan.Last.Started {
		t.Fatalf("%+v %v", v.Scan, err)
	}
	// a found file that is gone (moved to the file trash) shows as such
	if err := os.MkdirAll(filepath.Join(m.wpDir(id), "wp-content", "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(m.wpDir(id), "wp-content", "uploads", "shell.php"), []byte("<?php"), 0o644)
	if v, _ := m.Scan(id); v.Last.Found[0].Gone || !v.Last.Found[1].Gone {
		t.Errorf("gone: %+v", v.Last.Found)
	}
	var fresh, scan string
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "freshclam") {
			fresh = j
		}
		if strings.Contains(j, "clamscan") {
			scan = j
		}
	}
	// signatures with network but no write access to the website; the scan with no network and the website read-only
	if strings.Contains(fresh, m.wpDir(id)) || !strings.Contains(fresh, "--cap-drop ALL") {
		t.Errorf("freshclam: %s", fresh)
	}
	for _, want := range []string{"--network none", m.wpDir(id) + ":/scan:ro", "--read-only", "--cap-drop ALL", "--cap-add DAC_READ_SEARCH", "--memory 2g"} {
		if !strings.Contains(scan, want) {
			t.Errorf("scan lacks %q: %s", want, scan)
		}
	}
	// signatures checked a moment ago: no second freshclam
	n := len(f.calls)
	job, _ = m.StartScan(id)
	waitJob(t, m, job)
	for _, c := range f.calls[n:] {
		if strings.Contains(strings.Join(c, " "), "freshclam") {
			t.Error("freshclam ran again within 12 hours")
		}
	}
}

func TestMalwareScanRefusesAndFails(t *testing.T) {
	m, f, id := readySite(t)
	m.memFreeFn = func() int { return 900 }
	if _, err := m.StartScan(id); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "free memory") {
		t.Fatalf("low memory: %v", err)
	}
	m.memFreeFn = func() int { return 4000 }
	m.scanMu.Lock()
	if _, err := m.StartScan(id); !errors.Is(err, ErrBusy) {
		t.Errorf("second scan: %v", err)
	}
	m.scanMu.Unlock()
	// no signatures and freshclam fails: the scan fails and says why
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "freshclam") {
			return []byte("ERROR: Can't download daily.cvd from database.clamav.net"), errors.New("exit 1"), true
		}
		return nil, nil, false
	}
	job, _ := m.StartScan(id)
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "daily.cvd") {
		t.Fatalf("%+v", v)
	}
	// with older signatures a failed update is only a warning
	db := filepath.Join(m.clamDir(), "db")
	if err := os.WriteFile(filepath.Join(db, "daily.cld"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(db, "daily.cld"), old, old)
	job, _ = m.StartScan(id)
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	if r := m.readScan(id); r == nil || !strings.Contains(r.Warning, "previous ones") {
		t.Fatalf("%+v", r)
	}
	if _, err := m.StartScan("nope"); err == nil {
		t.Error("unknown website scanned")
	}
}

func accessLine(ip, host, method, path string, status int, at time.Time) string {
	return fmt.Sprintf(`{"ClientHost":%q,"RequestHost":%q,"RequestMethod":%q,"RequestPath":%q,"DownstreamStatus":%d,"StartUTC":%q}`+"\n",
		ip, host, method, path, status, at.UTC().Format(time.RFC3339Nano))
}

func TestLoginAttempts(t *testing.T) {
	m, _, id := readySite(t)
	log := filepath.Join(t.TempDir(), "access.log")
	m.cfg.AccessLog = log
	if v := m.Logins(Site{ID: id}); v.Active {
		t.Fatal("active without a log")
	}
	now := time.Now()
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString(accessLine("198.51.100.7", "voltix.id", "POST", "/wp-login.php", 200, now.Add(-time.Duration(i)*time.Minute)))
	}
	b.WriteString(accessLine("198.51.100.7", "voltix.id", "POST", "/wp-login.php", 302, now))
	b.WriteString(accessLine("::ffff:192.0.2.9", "VOLTIX.ID:443", "POST", "/xmlrpc.php?x=1", 200, now))
	b.WriteString(accessLine("192.0.2.10", "voltix.id", "POST", "/login", 429, now.Add(-3*24*time.Hour)))
	b.WriteString(accessLine("192.0.2.11", "voltix.id", "GET", "/wp-login.php", 200, now))                              // not a login try
	b.WriteString(accessLine("192.0.2.12", "voltix.id", "POST", "/wp-admin/post.php", 200, now))                        // not a login page
	b.WriteString(accessLine("192.0.2.13", "other.example", "POST", "/wp-login.php", 200, now))                         // not a website here
	b.WriteString(accessLine("192.0.2.14", "voltix.id", "POST", "/wp-login.php", 200, now.Add(-9*24*time.Hour)))        // too old
	b.WriteString(`{"ClientHost":"192.0.2.15","RequestHost":"voltix.id","RequestMethod":"POST","RequestPath":"/login"`) // no newline yet
	if err := os.WriteFile(log, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(id)
	v := m.Logins(s)
	if !v.Active || v.Day != 32 || v.DayFail != 30 || v.Week != 33 || len(v.IPs) != 3 {
		t.Fatalf("%+v", v)
	}
	top := v.IPs[0]
	if top.IP != "198.51.100.7" || top.Day != 31 || top.DayFail != 30 || top.LastPath != "/wp-login.php" || top.Blocked {
		t.Fatalf("%+v", top)
	}
	if v.IPs[1].IP != "192.0.2.9" || v.IPs[2].IP != "192.0.2.10" || v.IPs[2].Day != 0 || v.IPs[2].Week != 1 {
		t.Fatalf("%+v", v.IPs)
	}
	// the half line is read once it is complete; a blocked network marks its addresses
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintf(f, `,"DownstreamStatus":401,"StartUTC":%q}`+"\n", now.UTC().Format(time.RFC3339Nano))
	f.Close()
	if err := m.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	s.BlockIPs = []string{"198.51.100.0/24"}
	v = m.Logins(s)
	if v.Day != 33 || v.DayFail != 31 || !v.IPs[0].Blocked {
		t.Fatalf("%+v", v)
	}
	// kept across a restart of the agent; a log that was emptied is read from the start again
	m2 := New(m.cfg, nil, nil)
	if err := os.WriteFile(log, []byte(accessLine("192.0.2.20", "voltix.id", "POST", "/wp-login.php", 200, now)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m2.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	if v := m2.Logins(s); v.Day != 34 {
		t.Fatalf("after restart: %+v", v)
	}
	if fi, err := os.Stat(m.loginFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestAccessLogEmptiedWhenBig(t *testing.T) {
	m, _, _ := readySite(t)
	log := filepath.Join(t.TempDir(), "access.log")
	m.cfg.AccessLog = log
	line := accessLine("192.0.2.1", "voltix.id", "GET", "/", 200, time.Now())
	big := strings.Repeat(line, accessLogMax/len(line)+10)
	if err := os.WriteFile(log, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(log); fi.Size() != 0 || m.loginDB().st.Offset != 0 {
		t.Fatalf("not emptied: %d %d", fi.Size(), m.loginDB().st.Offset)
	}
}

func TestLoginPathsAndFailures(t *testing.T) {
	for _, p := range []string{"/wp-login.php", "/xmlrpc.php", "/login", "/login/", "/administrator/index.php", "/user/login", "/api/auth/login", "/WP-LOGIN.PHP"} {
		if !loginRe.MatchString(p) {
			t.Errorf("%s not a login page", p)
		}
	}
	for _, p := range []string{"/", "/wp-admin/", "/blog/login-tips", "/wp-login.php.bak"} {
		if loginRe.MatchString(p) {
			t.Errorf("%s is a login page", p)
		}
	}
	if !loginFailed("/wp-login.php", 200) || loginFailed("/wp-login.php", 302) || loginFailed("/login", 200) || !loginFailed("/login", 429) {
		t.Error("failure rules")
	}
}
