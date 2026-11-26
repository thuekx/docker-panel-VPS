package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const sbkName = "nostek-server-20261126-170000.npbk"

func (f *fakeAgent) serverBackups(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	if r.Method == "POST" {
		f.sbkCalls = append(f.sbkCalls, strings.TrimSpace(r.Method+" "+p+" "+string(b)))
	}
	switch {
	case strings.HasPrefix(p, "/v1/jobs/"):
		write(200, f.sbkJob)
	case p == "/v1/server-backups" && r.Method == "GET":
		write(200, agent.ServerBackups{Free: 40 << 30, Need: 2 << 30, Backups: []agent.ServerBackup{{Name: sbkName, Size: 900 << 20, Created: 1764151200}}})
	case p == "/v1/server-backups" && r.Method == "POST":
		f.sbkSeen, _ = os.ReadFile(f.sbkCopy)
		write(202, map[string]string{"job": "j5bc0000000000001"})
	case p == "/v1/server-backups/"+sbkName+"/delete":
		write(200, map[string]bool{"ok": true})
	case p == "/v1/server-backups/"+sbkName:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+sbkName+`"`)
		w.WriteHeader(200)
		w.Write([]byte("NPBK1\nSEALED"))
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

func TestServerBackupPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	f.sbkCopy = filepath.Join(filepath.Dir(e.st.Path), "server-backup.db")
	if code, _, h := e.get(e.c, "/settings/backup"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	f.sbkJob = agent.Job{ID: "j5bc0000000000001", Kind: "server-backup", State: "running", Steps: []string{"panel", "websites"}, Progress: "voltix.id (1/2)"}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?job=j5bc0000000000001", "?done=delete", "?err=x"} {
			code, body, _ := e.get(e.c, "/settings/backup"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/backup"`) || missingKey.MatchString(body) {
			t.Errorf("settings (%s) lacks the server backup card", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/backup?job=j5bc0000000000001")
	for _, want := range []string{`href="/settings/backup/file/` + sbkName + `"`, "voltix.id (1/2)", `data-job="/sites/job/j5bc0000000000001/status"`, "--restore", "40.0 GB free"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	const pass = "correct horse battery staple"
	try := func(form url.Values) (int, string) {
		form.Set("op", "create")
		code, _, h := e.post(e.c, "/settings/backup", form, "/settings/backup")
		return code, h.Get("Location")
	}
	for name, form := range map[string]url.Values{
		"wrong password": {"password": {"nope"}, "passphrase": {pass}, "passphrase2": {pass}},
		"short":          {"password": {pw}, "passphrase": {"elevenchars"}, "passphrase2": {"elevenchars"}},
		"mismatch":       {"password": {pw}, "passphrase": {pass}, "passphrase2": {pass + "!"}},
	} {
		if code, loc := try(form); code != 303 || !strings.Contains(loc, "err=") {
			t.Errorf("%s: %d %s", name, code, loc)
		}
	}
	if len(f.sbkCalls) != 0 {
		t.Fatalf("agent asked: %v", f.sbkCalls)
	}
	code, loc := try(url.Values{"password": {pw}, "passphrase": {pass}, "passphrase2": {pass}})
	if code != 303 || loc != "/settings/backup?job=j5bc0000000000001#job" {
		t.Fatalf("create %d %s", code, loc)
	}
	if len(f.sbkCalls) != 1 || !strings.Contains(f.sbkCalls[0], `"passphrase":"`+pass+`"`) || !strings.Contains(f.sbkCalls[0], `"panel_version":"test"`) {
		t.Fatalf("agent call %v", f.sbkCalls)
	}
	if !strings.HasPrefix(string(f.sbkSeen), "SQLite format 3") {
		t.Fatalf("no database copy for the agent: %q", f.sbkSeen)
	}
	if fi, err := os.Stat(f.sbkCopy); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy mode %v %v", fi, err)
	}
	// without the CSRF token nothing happens
	resp, _ := e.c.PostForm(e.ts.URL+"/settings/backup", url.Values{"op": {"delete"}, "name": {sbkName}})
	resp.Body.Close()
	if resp.StatusCode != 403 || len(f.sbkCalls) != 1 {
		t.Fatalf("no csrf: %d %v", resp.StatusCode, f.sbkCalls)
	}
	if code, _, h := e.post(e.c, "/settings/backup", url.Values{"op": {"delete"}, "name": {sbkName}}, "/settings/backup"); code != 303 || h.Get("Location") != "/settings/backup?done=delete" {
		t.Fatalf("delete %d %s", code, h.Get("Location"))
	}
	if code, _, _ := e.post(e.c, "/settings/backup", url.Values{"op": {"delete"}, "name": {"../panel.db"}}, "/settings/backup"); code != 400 {
		t.Fatalf("bad name %d", code)
	}
	code, body, h := e.get(e.c, "/settings/backup/file/"+sbkName)
	if code != 200 || body != "NPBK1\nSEALED" || !strings.Contains(h.Get("Content-Disposition"), sbkName) {
		t.Fatalf("download %d %q %v", code, body, h)
	}
	if code, _, _ := e.get(e.c, "/settings/backup/file/panel.db"); code != 404 {
		t.Fatalf("bad download name %d", code)
	}
	var audit string
	rows, _ := e.st.DB.Query(`SELECT action || ' ' || detail FROM audit`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		audit += s + "\n"
	}
	rows.Close()
	if !strings.Contains(audit, "server.backup job j5bc0000000000001") || !strings.Contains(audit, "server.backup refused: wrong password") || !strings.Contains(audit, "server.backup.download") || strings.Contains(audit, pass) {
		t.Fatalf("audit:\n%s", audit)
	}
}
