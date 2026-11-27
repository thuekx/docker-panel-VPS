package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) update(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	if r.Method == "POST" {
		f.updCalls = append(f.updCalls, string(b))
		if strings.Contains(string(b), `"tag":"v1.99.0"`) {
			write(400, map[string]string{"error": "v1.99.0 is not a published release"})
			return
		}
		write(202, map[string]string{"job": "jupd000000000001"})
		return
	}
	f.updGets = append(f.updGets, "GET "+r.URL.RawQuery)
	write(200, f.upd)
}

func TestUpdatePage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/settings/update"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	pub := time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)
	f.upd = agent.UpdateView{Current: "v1.37.0", Releases: []agent.Release{
		{Tag: "v1.38.0", Name: "v1.38.0 — update from the panel", Notes: "### Added\n- <script>alert(1)</script>", Published: pub, Newer: true},
		{Tag: "v1.37.0", Name: "v1.37.0", Notes: "passkeys"},
	}}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?fresh=1", "?err=x"} {
			code, body, _ := e.get(e.c, "/settings/update"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/update"`) || missingKey.MatchString(body) {
			t.Errorf("settings (%s) lacks the update card", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/update")
	for _, want := range []string{"Running v1.37.0", `value="v1.38.0" checked`, "&lt;script&gt;alert(1)&lt;/script&gt;", "27 Nov 2026", `data-reveal="#upd-pass"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `value="v1.37.0"`) {
		t.Error("the running release is offered")
	}
	if !strings.Contains(strings.Join(f.updGets, ","), "GET ,GET fresh=1") {
		t.Errorf("calls %v", f.updGets)
	}
	// refusals: nothing reaches the agent
	f.updCalls = nil
	const pass = "correct horse battery staple"
	try := func(form url.Values) string {
		_, _, h := e.post(e.c, "/settings/update", form, "/settings/update")
		return h.Get("Location")
	}
	for name, form := range map[string]url.Values{
		"wrong password": {"tag": {"v1.38.0"}, "password": {"nope"}},
		"no tag":         {"tag": {"main"}, "password": {pw}},
		"short":          {"tag": {"v1.38.0"}, "password": {pw}, "backup": {"1"}, "passphrase": {"elevenchars"}, "passphrase2": {"elevenchars"}},
		"mismatch":       {"tag": {"v1.38.0"}, "password": {pw}, "backup": {"1"}, "passphrase": {pass}, "passphrase2": {pass + "!"}},
	} {
		if loc := try(form); !strings.Contains(loc, "err=") {
			t.Errorf("%s: %s", name, loc)
		}
	}
	if len(f.updCalls) != 0 {
		t.Fatalf("refused forms reached the agent: %v", f.updCalls)
	}
	dir := filepath.Dir(e.st.Path)
	if loc := try(url.Values{"tag": {"v1.38.0"}, "password": {pw}}); loc != "/settings/update#run" {
		t.Fatalf("start: %s", loc)
	}
	if len(f.updCalls) != 1 || !strings.Contains(f.updCalls[0], `"tag":"v1.38.0"`) || !strings.Contains(f.updCalls[0], `"passphrase":""`) {
		t.Fatalf("calls %v", f.updCalls)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "update.db")); err != nil || !strings.HasPrefix(string(b), "SQLite format 3") {
		t.Fatalf("no database copy for the roll-back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server-backup.db")); err == nil {
		t.Fatal("a backup copy without a backup")
	}
	if loc := try(url.Values{"tag": {"v1.38.0"}, "password": {pw}, "backup": {"1"}, "passphrase": {pass}, "passphrase2": {pass}}); loc != "/settings/update#run" {
		t.Fatalf("start with backup: %s", loc)
	}
	if !strings.Contains(f.updCalls[1], `"passphrase":"`+pass+`"`) {
		t.Fatalf("calls %v", f.updCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, "server-backup.db")); err != nil {
		t.Fatal("no copy for the server backup")
	}
	os.Remove(filepath.Join(dir, "update.db"))
	if loc := try(url.Values{"tag": {"v1.99.0"}, "password": {pw}}); !strings.Contains(loc, "not+a+published+release") {
		t.Fatalf("agent refusal: %s", loc)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.db")); err == nil {
		t.Fatal("the copy stays after a refusal")
	}
	audit := e.auditText()
	if strings.Contains(audit, pass) || !strings.Contains(audit, "v1.38.0 from") || !strings.Contains(audit, "server backup first") {
		t.Fatalf("audit %s", audit)
	}

	// a running update: the card polls; the status answer carries the lines
	f.upd.Status = &agent.UpdateStatus{Phase: "RUNNING", Tag: "v1.38.0", From: "v1.37.0", Running: true, Started: 1764200000, Lines: []string{"PROGRESS 2/4 agent v1.38.0"}}
	_, body, _ = e.get(e.c, "/settings/update")
	for _, want := range []string{`data-update="/settings/update/status"`, `data-busy-run="1"`, "Update v1.37.0 → v1.38.0", "PROGRESS 2/4 agent v1.38.0", `disabled>Update now`} {
		if !strings.Contains(body, want) {
			t.Errorf("running page lacks %q", want)
		}
	}
	code, js, _ := e.get(e.c, "/settings/update/status")
	if code != 200 || !strings.Contains(js, `"busy":true`) || !strings.Contains(js, `"phase":"RUNNING"`) || !strings.Contains(js, "PROGRESS 2/4") {
		t.Fatalf("status %d %s", code, js)
	}
	f.upd.Status = &agent.UpdateStatus{Phase: "ROLLED_BACK", Tag: "v1.38.0", From: "v1.37.0", Result: "panel VERIFY failed; v1.37.0 is running again", Ended: 1764200300}
	_, body, _ = e.get(e.c, "/settings/update")
	if !strings.Contains(body, "Rolled back") || !strings.Contains(body, "v1.37.0 is running again") || strings.Contains(body, `data-busy-run="1"`) {
		t.Error("rolled-back run not shown")
	}
	f.upd = agent.UpdateView{Current: "v1.37.0", Ready: "the running release has no updater yet: update to it once with the runbook",
		Releases: []agent.Release{{Tag: "v1.38.0", Name: "v1.38.0", Newer: true}}}
	_, body, _ = e.get(e.c, "/settings/update")
	if !strings.Contains(body, "not possible on this server yet") || !strings.Contains(body, `disabled>Update now`) {
		t.Error("not-ready server not shown")
	}
	js2, _ := os.ReadFile("../../web/static/js/app.js")
	for _, want := range []string{"function updater()", "function reveal()", "reveal(); updater(); toasts(); });"} {
		if !strings.Contains(string(js2), want) {
			t.Errorf("app.js: no %s", want)
		}
	}
}

func (e *env) auditText() string {
	var out string
	rows, _ := e.st.DB.Query(`SELECT action || ' ' || detail FROM audit`)
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out += s + "\n"
	}
	return out
}
