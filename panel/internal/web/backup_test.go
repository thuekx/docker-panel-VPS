package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const bakName = "manual-20261112-150405.tar.gz"

// backups answers the backup routes of the fake agent.
func (f *fakeAgent) backups(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	if r.Method == "POST" {
		f.bakCalls = append(f.bakCalls, strings.TrimSpace(r.Method+" "+strings.TrimPrefix(p, "/v1/sites/voltix-id/backups")+" "+string(b)))
	}
	switch {
	case strings.HasPrefix(p, "/v1/jobs/"):
		write(200, f.bakJob)
	case p == "/v1/sites/voltix-id/backups" && r.Method == "GET":
		if f.bakDown {
			write(500, map[string]string{"error": "agent cannot read /srv/nostek/sites/voltix-id/backups"})
			return
		}
		write(200, agent.Backups{Daily: true, Keep: 7, Free: 40 << 30, Backups: []agent.SiteBackup{
			{Name: bakName, Kind: "manual", Size: 52 << 20, Created: 1762934645},
			{Name: "daily-20261112-020000.tar.gz", Kind: "daily", Size: 51 << 20, Created: 1762887600},
			{Name: "before-restore-20261111-100000.tar.gz", Kind: "before-restore", Size: 50 << 20, Created: 1762830000}}})
	case p == "/v1/sites/voltix-id/backups" && r.Method == "POST":
		write(202, map[string]string{"job": "jbac0000000000001"})
	case p == "/v1/sites/voltix-id/backups/schedule":
		write(200, map[string]bool{"ok": true})
	case strings.HasSuffix(p, "/restore"):
		write(202, map[string]string{"job": "jbac0000000000002"})
	case strings.HasSuffix(p, "/delete"):
		write(200, map[string]bool{"ok": true})
	case p == "/v1/sites/voltix-id/backups/"+bakName:
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="voltix-id-`+bakName+`"`)
		w.WriteHeader(200)
		w.Write([]byte("\x1f\x8bARCHIVE"))
	default:
		write(404, map[string]string{"error": "backup not found"})
	}
}

func TestBackupPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/backup/voltix-id"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("backup page reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	f.bakJob = agent.Job{ID: "jbac0000000000002", Kind: "restore", Site: "voltix-id", State: "running", Steps: []string{"safety database", "safety files", "restore database"}}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?restore=" + bakName, "?job=jbac0000000000002", "?done=schedule", "?done=delete"} {
			code, body, _ := e.get(e.c, "/backup/voltix-id"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/backup/voltix-id")
	for _, want := range []string{"Make a backup now", "safety copy", "daily", `href="/backup/voltix-id/file/` + bakName + `"`, `href="/backup/voltix-id?restore=` + bakName + `#restore"`, "the last 7 days", `aria-current="page"><span aria-hidden="true"><svg`, "03:30 WIB"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="job"`) || strings.Contains(body, `id="restore"`) {
		t.Error("job card or restore form without a reason")
	}
	_, body, _ = e.get(e.c, "/backup/voltix-id?restore="+bakName)
	if !strings.Contains(body, `id="restore"`) || !strings.Contains(body, "Type voltix.id to confirm") || !strings.Contains(body, `value="`+bakName+`"`) {
		t.Error("restore form not shown")
	}
	if _, body, _ = e.get(e.c, "/backup/voltix-id?restore=site.json"); strings.Contains(body, `id="restore"`) {
		t.Error("restore form for a name not in the list")
	}
	// a running restore is followed with its own steps; "done <name>" is the result, not a step
	_, body, _ = e.get(e.c, "/backup/voltix-id?job=jbac0000000000002")
	if !strings.Contains(body, `data-job="/sites/job/jbac0000000000002/status"`) || !strings.Contains(body, "Putting the database back") || !strings.Contains(body, "Switching the website") {
		t.Error("running restore not shown")
	}
	f.bakJob.State, f.bakJob.Steps = "done", []string{"safety database", "safety files", "restore database", "done before-restore-20261112-160000.tar.gz"}
	_, body, _ = e.get(e.c, "/backup/voltix-id?job=jbac0000000000002")
	if !strings.Contains(body, "kept as the safety copy before-restore-20261112-160000.tar.gz") || strings.Contains(body, "Switching the website") || strings.Contains(body, "data-job=") {
		t.Error("finished database restore shown wrong")
	}
	f.bakJob.State, f.bakJob.Error = "failed", "database restore failed: ERROR 1064"
	_, body, _ = e.get(e.c, "/backup/voltix-id?job=jbac0000000000002")
	if !strings.Contains(body, "restore the newest safety copy") || !strings.Contains(body, "Technical details") {
		t.Error("failed restore not explained")
	}
	// another site's job is not shown on this page
	f.bakJob.Site = "other-id"
	if _, body, _ = e.get(e.c, "/backup/voltix-id?job=jbac0000000000002"); strings.Contains(body, `id="job"`) {
		t.Error("job of another site shown")
	}
	f.bakDown = true
	if code, body, _ := e.get(e.c, "/backup/voltix-id"); code != 200 || !strings.Contains(body, "could not be read right now") || !strings.Contains(body, "/srv/nostek") {
		t.Errorf("agent error not shown: %d", code)
	}
	if code, _, _ := e.get(e.c, "/backup/Bad_Id"); code != 404 {
		t.Errorf("bad id answered %d", code)
	}
}

func TestBackupActions(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, page, _ := e.get(e.c, "/backup/voltix-id")
	if code, _, _ := e.postWith(e.c, "/backup/voltix-id", url.Values{"op": {"backup"}}, ""); code != 403 {
		t.Fatalf("no CSRF answered %d", code)
	}
	steps := []struct {
		form url.Values
		loc  string
		want string
	}{
		{url.Values{"op": {"backup"}}, "/backup/voltix-id?job=jbac0000000000001#job", "POST"},
		{url.Values{"op": {"schedule"}, "daily": {"1"}, "keep": {"14"}}, "/backup/voltix-id?done=schedule", `POST /schedule {"daily":true,"keep":14}`},
		{url.Values{"op": {"schedule"}, "keep": {"3"}}, "/backup/voltix-id?done=schedule", `POST /schedule {"daily":false,"keep":3}`},
		{url.Values{"op": {"restore"}, "name": {bakName}, "what": {"database"}, "confirm": {" Voltix.ID "}}, "/backup/voltix-id?job=jbac0000000000002#job", `POST /` + bakName + `/restore {"what":"database"}`},
		{url.Values{"op": {"delete"}, "name": {bakName}}, "/backup/voltix-id?done=delete", `POST /` + bakName + `/delete`},
	}
	for _, s := range steps {
		code, _, h := e.postWith(e.c, "/backup/voltix-id", s.form, page)
		if code != 303 || h.Get("Location") != s.loc {
			t.Errorf("%v: %d %s", s.form, code, h.Get("Location"))
		}
		if got := f.bakCalls[len(f.bakCalls)-1]; got != s.want {
			t.Errorf("agent got %q, want %q", got, s.want)
		}
	}
	n := len(f.bakCalls)
	// a typed domain that does not match restores nothing
	code, _, h := e.postWith(e.c, "/backup/voltix-id", url.Values{"op": {"restore"}, "name": {bakName}, "what": {"all"}, "confirm": {"voltix.com"}}, page)
	if code != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Errorf("mismatch: %d %s", code, h.Get("Location"))
	}
	for _, bad := range []url.Values{
		{"op": {"restore"}, "name": {bakName}, "what": {"everything"}, "confirm": {"voltix.id"}},
		{"op": {"restore"}, "name": {"../site.json"}, "what": {"all"}, "confirm": {"voltix.id"}},
		{"op": {"delete"}, "name": {"site.json"}},
		{"op": {"schedule"}, "keep": {"31"}},
		{"op": {"schedule"}, "keep": {"x"}},
		{"op": {"shell"}},
	} {
		if code, _, _ := e.postWith(e.c, "/backup/voltix-id", bad, page); code != 400 {
			t.Errorf("%v answered %d", bad, code)
		}
	}
	if len(f.bakCalls) != n {
		t.Errorf("refused forms reached the agent: %v", f.bakCalls[n:])
	}
	// download streams the archive through
	code, body, h := e.get(e.c, "/backup/voltix-id/file/"+bakName)
	if code != 200 || body != "\x1f\x8bARCHIVE" || !strings.Contains(h.Get("Content-Disposition"), bakName) {
		t.Errorf("download: %d %v", code, h)
	}
	for _, bad := range []string{"site.json", "manual-1.tar.gz", "..%2Fsite.json"} {
		if code, _, _ := e.get(e.c, "/backup/voltix-id/file/"+bad); code != 404 {
			t.Errorf("download %s answered %d", bad, code)
		}
	}
	_, audit, _ := e.get(e.c, "/audit")
	for _, want := range []string{"backup.start", "backup.schedule", "backup.restore", "backup.delete", "backup.download"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %s", want)
		}
	}
}

func TestJobProgressOfBackupJobs(t *testing.T) {
	rows, pct := jobProgress(agent.Job{Kind: "backup", State: "running", Steps: []string{"copy database"}})
	if len(rows) != 2 || rows[0].State != "active" || rows[1].State != "todo" || pct != 0 {
		t.Fatalf("running backup: %v %d", rows, pct)
	}
	rows, pct = jobProgress(agent.Job{Kind: "restore", State: "done", Steps: []string{"safety database", "safety files", "restore database", "done before-restore-x"}})
	if len(rows) != 3 || pct != 100 || rows[2].Key != "restore database" {
		t.Fatalf("done restore: %v %d", rows, pct)
	}
	rows, _ = jobProgress(agent.Job{Kind: "restore", State: "failed", Steps: []string{"safety database"}})
	if len(rows) != 5 || rows[0].State != "failed" {
		t.Fatalf("failed restore: %v", rows)
	}
	if rows, pct := jobProgress(agent.Job{Kind: "backup", State: "done"}); len(rows) != 0 || pct != 100 {
		t.Fatalf("empty done: %v %d", rows, pct)
	}
	rows, _ = jobProgress(agent.Job{State: "running", Steps: []string{"files"}})
	if len(rows) != 4 {
		t.Fatalf("create job lost its steps: %v", rows)
	}
}
