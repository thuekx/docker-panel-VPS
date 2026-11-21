package waf

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		var e Event
		if json.Unmarshal([]byte(l), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

type seen struct {
	mu   sync.Mutex
	last *http.Request
	body int
}

func backend(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		s.mu.Lock()
		s.last, s.body = r, int(n)
		s.mu.Unlock()
		io.WriteString(w, "site "+r.Host)
	}))
	t.Cleanup(ts.Close)
	return ts, s
}

func writeSite(t *testing.T, dir string, s Site) {
	t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, s.Site+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func visit(sv http.Handler, method, host, target string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.Host = host
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("User-Agent", "Mozilla/5.0 (test)")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	sv.ServeHTTP(w, r)
	return w
}

const sqli = "/?id=1%27%20OR%20%271%27=%271%27%20UNION%20SELECT%20password%20FROM%20users--"

func TestWAF(t *testing.T) {
	ts, got := backend(t)
	dir := t.TempDir()
	logb := &syncBuf{}
	sv := New(dir, logb)
	writeSite(t, dir, Site{Site: "a-example-test", Hosts: []string{"a.example.test", "www.a.example.test"}, Upstream: ts.URL,
		Mode: "block", Paranoia: 1, Skip: []string{"/wp-admin/"}})
	writeSite(t, dir, Site{Site: "b-example-test", Hosts: []string{"b.example.test"}, Upstream: ts.URL, Mode: "detect", Paranoia: 1})
	if err := sv.Load(); err != nil {
		t.Fatal(err)
	}
	if w := visit(sv, "GET", "a.example.test", "/hello?x=1", nil); w.Code != 200 || w.Body.String() != "site a.example.test" {
		t.Fatalf("plain visit: %d %q", w.Code, w.Body.String())
	}
	got.mu.Lock()
	if got.last.Header.Get("X-Forwarded-For") != "203.0.113.9" || got.last.Header.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("forwarded headers: %v", got.last.Header)
	}
	got.mu.Unlock()
	if w := visit(sv, "GET", "A.EXAMPLE.TEST:443", sqli, nil); w.Code != 403 {
		t.Fatalf("SQL injection not blocked: %d", w.Code)
	}
	if w := visit(sv, "GET", "b.example.test", sqli, nil); w.Code != 200 {
		t.Fatalf("detect mode blocked: %d", w.Code)
	}
	if w := visit(sv, "GET", "a.example.test", "/wp-admin/"+sqli[1:], nil); w.Code != 200 {
		t.Fatalf("skipped path checked: %d", w.Code)
	}
	if w := visit(sv, "GET", "c.example.test", "/", nil); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unknown host: %d", w.Code)
	}
	// a POST body with an attack is checked; a large upload passes whole
	if w := visit(sv, "POST", "a.example.test", "/form", strings.NewReader("q=<script>alert(document.cookie)</script>")); w.Code != 403 {
		t.Errorf("XSS in a body not blocked: %d", w.Code)
	}
	var big bytes.Buffer
	mw := multipart.NewWriter(&big)
	fw, _ := mw.CreateFormFile("async-upload", "photo.jpg")
	_, _ = fw.Write(bytes.Repeat([]byte{0xff, 0xd8, 'a', 'b'}, 5<<20))
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/wp-admin/async-upload.php", bytes.NewReader(big.Bytes()))
	r.Host, r.Header = "a.example.test", http.Header{"Content-Type": {mw.FormDataContentType()}, "Content-Length": {strconv.Itoa(big.Len())},
		"User-Agent": {"Mozilla/5.0"}, "Accept": {"*/*"}}
	w := httptest.NewRecorder()
	sv.ServeHTTP(w, r)
	got.mu.Lock()
	n := got.body
	got.mu.Unlock()
	if w.Code != 200 || n != big.Len() {
		t.Errorf("large upload: %d, %d bytes reached the website", w.Code, n)
	}
	ev := logb.events()
	var blocked, detected bool
	for _, e := range ev {
		if e.Site == "a-example-test" && e.Action == "blocked" && e.Rule == 949110 && e.IP == "203.0.113.9" {
			blocked = true
		}
		if e.Site == "b-example-test" && e.Action == "detected" && e.Rule >= 942000 && e.Rule < 943000 {
			detected = true
		}
		if e.Rule < 910000 || (e.Rule >= 980000) {
			t.Errorf("bookkeeping rule logged: %+v", e)
		}
	}
	if !blocked || !detected {
		t.Fatalf("events: %+v", ev)
	}
	// removing the rules that matched lets the visit through; a broken file keeps the previous rules
	var ids []int
	for _, e := range ev {
		if e.Site == "a-example-test" && e.Action != "blocked" && strings.Contains(e.URI, "UNION") {
			ids = append(ids, e.Rule)
		}
	}
	time.Sleep(10 * time.Millisecond) // a new modification time
	writeSite(t, dir, Site{Site: "a-example-test", Hosts: []string{"a.example.test"}, Upstream: ts.URL, Mode: "block", Paranoia: 1, Exclude: ids})
	if err := sv.Load(); err != nil {
		t.Fatal(err)
	}
	if w := visit(sv, "GET", "a.example.test", sqli, nil); w.Code != 200 {
		t.Errorf("excluded rules %v still block: %d", ids, w.Code)
	}
	if w := visit(sv, "GET", "www.a.example.test", "/", nil); w.Code != http.StatusMisdirectedRequest {
		t.Errorf("a host taken off still served: %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(dir, "b-example-test.json"), []byte(`{"site":"b-example-test","hosts":["b.example.test"],"upstream":"ftp://x","mode":"detect","paranoia":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sv.Load(); err == nil {
		t.Error("a broken file was accepted")
	}
	if w := visit(sv, "GET", "b.example.test", "/", nil); w.Code != 200 {
		t.Errorf("previous rules lost: %d", w.Code)
	}
	os.Remove(filepath.Join(dir, "b-example-test.json"))
	_ = sv.Load()
	if got := strings.Join(sv.Sites(), ","); got != "a-example-test" {
		t.Errorf("sites %s", got)
	}
}

func TestCheckAndDirectives(t *testing.T) {
	good := Site{Site: "x", Hosts: []string{"x.test"}, Upstream: "http://np-site-x-web-1:80", Mode: "block", Paranoia: 2, Exclude: []int{942100}, Skip: []string{"/wp-admin/", "/"}}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Site{
		{Site: "X!", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "block", Paranoia: 1},
		{Site: "x", Upstream: good.Upstream, Mode: "block", Paranoia: 1},
		{Site: "x", Hosts: good.Hosts, Upstream: "http://h:80/path", Mode: "block", Paranoia: 1},
		{Site: "x", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "on", Paranoia: 1},
		{Site: "x", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "block", Paranoia: 3},
		{Site: "x", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "block", Paranoia: 1, Exclude: []int{900000}},
		{Site: "x", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "block", Paranoia: 1, Skip: []string{"/a\" \"id:1,ctl:ruleEngine=Off"}},
		{Site: "x", Hosts: good.Hosts, Upstream: good.Upstream, Mode: "block", Paranoia: 1, Skip: []string{"/a/../b"}},
	} {
		if bad.Check() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	d := good.Directives()
	for _, want := range []string{"SecRuleEngine On", "tx.blocking_paranoia_level=2", "SecRuleRemoveById 942100",
		`"@beginsWith /wp-admin/" "id:1000,`, `"@unconditionalMatch /" "id:1001,`, "SecResponseBodyAccess Off"} {
		if !strings.Contains(d, want) {
			t.Errorf("directives lack %q:\n%s", want, d)
		}
	}
	if strings.Index(d, "SecRuleRemoveById") < strings.Index(d, "Include @owasp_crs") {
		t.Error("rules removed before they are loaded")
	}
}

func TestEventCap(t *testing.T) {
	b := &syncBuf{}
	now := time.Unix(1700000000, 0)
	l := &logger{w: b, now: func() time.Time { return now }}
	for i := 0; i < eventsPerMinute+50; i++ {
		l.write(Event{Site: "a"})
	}
	l.write(Event{Site: "b"})
	if n := len(b.events()); n != eventsPerMinute+1 {
		t.Fatalf("%d lines", n)
	}
	now = now.Add(time.Minute)
	l.write(Event{Site: "a"})
	if n := len(b.events()); n != eventsPerMinute+2 {
		t.Fatalf("next minute: %d lines", n)
	}
}
