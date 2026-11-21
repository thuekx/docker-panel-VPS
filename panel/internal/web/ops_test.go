package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) opsAPI(r *http.Request, write func(int, any)) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/metrics"):
		m := f.metrics
		m.Range = 7
		if r.URL.Query().Get("days") == "1" {
			m.Range = 1
		}
		write(200, m)
	case r.Method == "POST":
		b, _ := io.ReadAll(r.Body)
		f.cronCalls = append(f.cronCalls, strings.TrimSpace(string(b)))
		switch {
		case strings.Contains(string(b), `"schedule":"bad"`):
			write(400, map[string]string{"error": "bad input: schedule \"bad\": a schedule has five fields"})
		case strings.Contains(string(b), `"op":"on"`):
			write(200, map[string]any{"id": "abc123", "changed": false})
		default:
			write(200, map[string]any{"id": "abc123", "changed": true})
		}
	default:
		write(200, f.cron)
	}
}

func TestMetricsPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.metrics = agent.Metrics{Active: true, Total: agent.StatDay{Visitors: 12, Requests: 340, Bots: 40, Bytes: 5 << 20, S4: 3, S5: 1},
		Days:  []agent.StatDay{{Day: "2026-11-21", Requests: 100, Visitors: 5}, {Day: "2026-11-22", Requests: 240, Visitors: 7, S4: 3, S5: 1, Bytes: 3 << 20}},
		Pages: []agent.StatCount{{Key: "/shop", N: 50}}, Refs: []agent.StatCount{{Key: "www.google.com", N: 9}}, Errors: []agent.StatCount{{Key: "404 /<x>", N: 3}},
		Raw: []string{`198.51.100.7 - - [22/Nov/2026:10:00:00 +0700] "GET /<script>" 404 0 "" "Mozilla"`}}
	f.metrics.Hours[10] = 20
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/metrics/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ := e.get(e.c, "/metrics/voltix-id")
	for _, want := range []string{`href="/metrics/voltix-id" aria-current="page"`, ">340<", "5.0 MB", "of which 40 from bots", "<code>/shop</code>", "www.google.com",
		"404 /&lt;x&gt;", `GET /&lt;script&gt;`, `href="/metrics/voltix-id?days=7"`, `data-bar="100"`, "3.0 MB", `href="/metrics/voltix-id/raw"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if i, j := strings.Index(body, "2026-11-22"), strings.Index(body, "2026-11-21"); i < 0 || j < i {
		t.Error("days not newest first")
	}
	code, raw, h := e.get(e.c, "/metrics/voltix-id/raw")
	if code != 200 || !strings.HasPrefix(raw, "198.51.100.7 - - [") || !strings.Contains(h.Get("Content-Disposition"), "voltix-id-access.log") || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Errorf("raw %d %q %v", code, raw, h)
	}
	f.metrics = agent.Metrics{}
	if _, body, _ := e.get(e.c, "/metrics/voltix-id"); !strings.Contains(body, "comes with the panel update") {
		t.Error("inactive banner")
	}
}

func TestCronPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.cron = agent.Cron{Supported: true, Where: "php", Max: 20, Jobs: []agent.CronJob{
		{ID: "abc123", Schedule: "*/5 * * * *", Command: "php artisan <x>", Next: 1795000000, Runs: []agent.CronRun{{Start: 1794999700, Code: 2, Output: "boom <b>", Millis: 30}}},
		{ID: "def456", Schedule: "@daily", Command: "php b.php", Off: true}}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/cron/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ := e.get(e.c, "/cron/voltix-id")
	for _, want := range []string{`href="/cron/voltix-id" aria-current="page"`, "Last run failed (code 2)", "boom &lt;b&gt;", "php artisan &lt;x&gt;", "Runs in: php.",
		`name="op" value="on"><input type="hidden" name="id" value="def456">`, `id="job-abc123"`, "Next: "} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, c := range []struct {
		form url.Values
		loc  string
		sent string
	}{
		{url.Values{"op": {"add"}, "schedule": {" */5 * * * * "}, "command": {" php a.php "}}, "done=add#job-abc123", `{"op":"add","schedule":"*/5 * * * *","command":"php a.php"}`},
		{url.Values{"op": {"run"}, "id": {"abc123"}}, "done=run", `{"op":"run","id":"abc123"}`},
		{url.Values{"op": {"on"}, "id": {"abc123"}}, "done=same", `{"op":"on","id":"abc123"}`},
		{url.Values{"op": {"remove"}, "id": {"abc123"}, "confirm": {"1"}}, "done=remove", `{"op":"remove","id":"abc123"}`},
		{url.Values{"op": {"add"}, "schedule": {"bad"}, "command": {"x"}}, "err=", `"schedule":"bad"`},
	} {
		n := len(f.cronCalls)
		code, _, h := e.post(e.c, "/cron/voltix-id", c.form, "/cron/voltix-id")
		if code != 303 || !strings.Contains(h.Get("Location"), c.loc) || len(f.cronCalls) != n+1 || !strings.Contains(f.cronCalls[n], c.sent) {
			t.Errorf("%v: %d %s %v", c.form, code, h.Get("Location"), f.cronCalls)
		}
	}
	n := len(f.cronCalls)
	if code, _, h := e.post(e.c, "/cron/voltix-id", url.Values{"op": {"remove"}, "id": {"abc123"}}, "/cron/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "err=") || len(f.cronCalls) != n {
		t.Errorf("remove without confirm: %d %v", code, h.Get("Location"))
	}
	f.cron = agent.Cron{Where: "web"}
	if _, body, _ := e.get(e.c, "/cron/voltix-id"); !strings.Contains(body, "static website has no programs") || strings.Contains(body, `value="add"`) {
		t.Error("static page")
	}
}
