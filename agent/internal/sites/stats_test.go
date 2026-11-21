package sites

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func visitLine(ip, host, path string, status int, size int64, ref, ua string, at time.Time) string {
	return fmt.Sprintf(`{"ClientHost":%q,"RequestHost":%q,"RequestMethod":"GET","RequestPath":%q,"DownstreamStatus":%d,"DownstreamContentSize":%d,"request_Referer":%q,"request_User-Agent":%q,"StartUTC":%q}`+"\n",
		ip, host, path, status, size, ref, ua, at.UTC().Format(time.RFC3339Nano))
}

func TestMetrics(t *testing.T) {
	m, _, id := readySite(t)
	log := filepath.Join(t.TempDir(), "access.log")
	m.cfg.AccessLog = log
	now := time.Now()
	ff := "Mozilla/5.0 Firefox/140.0"
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(visitLine("198.51.100.7", "voltix.id", "/?p=1", 200, 1000, "https://www.google.com/search?q=x", ff, now))
	}
	b.WriteString(visitLine("198.51.100.8", "VOLTIX.ID:443", "/shop", 200, 500, "https://voltix.id/", ff, now))
	b.WriteString(visitLine("198.51.100.8", "voltix.id", "/gone", 404, 100, "", ff, now))
	b.WriteString(visitLine("198.51.100.9", "voltix.id", "/x.php", 502, 50, "", ff, now))
	b.WriteString(visitLine("203.0.113.5", "voltix.id", "/", 200, 1000, "", "Googlebot/2.1", now))
	b.WriteString(visitLine("203.0.113.6", "voltix.id", "/", 301, 0, "", ff, now.Add(-48*time.Hour)))
	b.WriteString(visitLine("203.0.113.7", "other.example", "/", 200, 9, "", ff, now))       // not a website here
	b.WriteString(accessLine("203.0.113.8", "voltix.id", "POST", "/wp-login.php", 200, now)) // the older log format still counts
	if err := os.WriteFile(log, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	v, err := m.Metrics(id, 7)
	if err != nil || !v.Active || v.Range != 7 || len(v.Days) != 7 {
		t.Fatalf("%+v %v", v, err)
	}
	td := v.Days[6]
	if td.Requests != 10 || td.Visitors != 3 || td.Bots != 2 || td.Bytes != 6650 || td.S2 != 8 || td.S4 != 1 || td.S5 != 1 {
		t.Errorf("today %+v", td)
	}
	if v.Days[4].Requests != 1 || v.Days[4].S3 != 1 || v.Total.Requests != 11 || v.Total.Visitors != 3 {
		t.Errorf("days %+v total %+v", v.Days, v.Total)
	}
	if len(v.Pages) == 0 || v.Pages[0] != (StatCount{"/", 6}) && v.Pages[0] != (StatCount{"/", 5}) {
		t.Errorf("pages %+v", v.Pages)
	}
	if len(v.Refs) != 1 || v.Refs[0] != (StatCount{"www.google.com", 5}) {
		t.Errorf("refs %+v (the website itself is not a referrer)", v.Refs)
	}
	if len(v.Errors) != 2 || v.Errors[0].Key != "404 /gone" && v.Errors[1].Key != "404 /gone" {
		t.Errorf("errors %+v", v.Errors)
	}
	h := 0
	for _, n := range v.Hours {
		h += n
	}
	if h != 10 || len(v.Raw) != 11 || !strings.HasPrefix(v.Raw[len(v.Raw)-1], `203.0.113.8 - - [`) || !strings.Contains(v.Raw[0], `"GET /?p=1" 200 1000 "https://www.google.com/search?q=x" "Mozilla/5.0 Firefox/140.0"`) {
		t.Errorf("hours %d raw %q", h, v.Raw)
	}
	// kept across a restart; addresses only as hashes on disk
	disk := mustRead(t, m.statsFile(id))
	if strings.Contains(disk, "198.51.100.8\"") && !strings.Contains(disk, "raw") {
		t.Error("plain address outside the raw lines")
	}
	if fi, _ := os.Stat(m.statsFile(id)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	m2 := New(m.cfg, nil, nil)
	if err := os.WriteFile(log, []byte(visitLine("198.51.100.7", "voltix.id", "/", 200, 10, "", ff, now)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m2.ReadLogins(); err != nil {
		t.Fatal(err)
	}
	if v, _ := m2.Metrics(id, 1); len(v.Days) != 1 || v.Days[0].Requests != 11 || v.Days[0].Visitors != 3 {
		t.Errorf("after restart %+v", v.Days)
	}
	if v, _ := m2.Metrics(id, 99); v.Range != 7 {
		t.Errorf("range %d", v.Range)
	}
	if _, err := m2.Metrics("nope", 7); err == nil {
		t.Error("unknown website")
	}
}

func TestStatLimits(t *testing.T) {
	s := &siteStats{seen: map[string]bool{}}
	own := func(h string) bool { return h == "voltix.id" }
	at := time.Date(2026, 11, 21, 16, 59, 0, 0, time.UTC) // 23:59 WIB
	for i := 0; i < 2*statPaths+10; i++ {
		s.add(statEntry{at: at, ip: "192.0.2.1", method: "GET", path: fmt.Sprintf("/p%d", i), status: 200, ua: "Mozilla"}, own)
	}
	if n := len(s.Days[0].Paths); n > 2*statPaths {
		t.Errorf("paths %d", n)
	}
	for i := 0; i < statRawKept+5; i++ {
		s.add(statEntry{at: at, ip: "192.0.2.2", method: "GET", path: "/", status: 200, ua: "Mozilla"}, own)
	}
	if len(s.Raw) != statRawKept || s.Days[0].Visitors != 2 {
		t.Errorf("raw %d visitors %d", len(s.Raw), s.Days[0].Visitors)
	}
	// the next day (WIB) starts a new count; the previous one is final
	s.add(statEntry{at: at.Add(2 * time.Minute), ip: "192.0.2.3", method: "GET", path: "/", status: 200, ua: "Mozilla"}, own)
	if len(s.Days) != 2 || s.Days[0].Visitors != 2 || s.Days[1].Visitors != 1 || s.Days[1].Day != "2026-11-22" {
		t.Errorf("days %+v %+v", s.Days[0].Visitors, s.Days[1])
	}
	// a late line of the earlier day counts there but not as a visitor of today
	s.add(statEntry{at: at, ip: "192.0.2.9", method: "GET", path: "/", status: 200, ua: "Mozilla"}, own)
	if s.Days[0].Req != 2*statPaths+10+statRawKept+5+1 || s.Days[1].Visitors != 1 {
		t.Errorf("late line %d %d", s.Days[0].Req, s.Days[1].Visitors)
	}
	if refHost("https://Voltix.id/a", own) != "" || refHost("android-app://x", own) != "x" || refHost("-", own) != "" || refHost("::", own) != "" {
		t.Error("refHost")
	}
}
