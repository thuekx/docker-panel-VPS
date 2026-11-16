package web

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// migrate answers the migrate routes of the fake agent.
func (f *fakeAgent) migrate(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	if strings.HasPrefix(p, "/v1/jobs/") {
		if f.migJob == nil {
			write(404, map[string]string{"error": "job not found"})
			return
		}
		write(200, f.migJob)
		return
	}
	body := string(b)
	if len(body) > 200 {
		body = body[:200]
	}
	f.migCalls = append(f.migCalls, strings.TrimSpace(r.Method+" "+strings.TrimPrefix(r.URL.RequestURI(), "/v1/sites/voltix-id/migrate")+" "+body))
	switch {
	case p == "/v1/sites/voltix-id/migrate" && r.Method == "GET":
		write(200, agent.MigrateFiles{Archive: 300 << 20, Job: f.migJob})
	case p == "/v1/sites/voltix-id/migrate" && r.Method == "POST":
		write(202, map[string]string{"job": "jmig000000000001"})
	case p == "/v1/sites/voltix-id/migrate/upload":
		write(200, map[string]int64{"bytes": int64(len(b))})
	case p == "/v1/sites/voltix-id/migrate/clear":
		write(200, map[string]bool{"ok": true})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

func TestMigratePage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	for _, p := range []string{"/migrate/voltix-id", "/migrate-plugin/nostek-migrate.zip"} {
		if code, _, h := e.get(e.c, p); code != 303 || h.Get("Location") != "/login" {
			t.Fatalf("%s reachable without login: %d", p, code)
		}
	}
	e.setupAdmin(e.c, "admin@example.test")
	jobs := []*agent.Job{nil,
		{ID: "jmig000000000001", Kind: "migrate", Site: "voltix-id", State: "running", Steps: []string{"inspect", "safety database", "safety files", "copy files"}, Progress: "12/340 MB"},
		{ID: "jmig000000000001", Kind: "migrate", Site: "voltix-id", State: "failed", Steps: []string{"inspect"}, Error: "choose: the backup holds several WordPress sites; choose one: backup-x/homedir/addon.example.com, backup-x/homedir/public_html"},
		{ID: "jmig000000000001", Kind: "migrate", Site: "voltix-id", State: "failed", Steps: []string{"inspect", "safety database"}, Error: "loading the database failed — the website can be put back with the backup before-migrate-20261116-080000.tar.gz"},
		{ID: "jmig000000000001", Kind: "migrate", Site: "voltix-id", State: "done", Steps: []string{"inspect", "check", "done https://old.example.com → https://voltix-id.preview.nostek.dev, WordPress 6.8.3"}},
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, j := range jobs {
			f.migJob = j
			for _, q := range []string{"", "?job=jmig000000000001", "?done=clear"} {
				code, body, _ := e.get(e.c, "/migrate/voltix-id"+q)
				if code != 200 || missingKey.MatchString(body) {
					t.Errorf("%s (%s, %v): %d or missing key %v", q, lang, j, code, missingKey.FindString(body))
				}
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	f.migJob = jobs[1]
	_, body, _ := e.get(e.c, "/migrate/voltix-id")
	for _, want := range []string{"Way 1: the Nostek Migrate plugin", "Way 2: a cPanel backup", `href="/migrate-plugin/nostek-migrate.zip"`, `data-job="/sites/job/jmig000000000001/status"`, "12/340 MB", "Uploaded: backup 300.0 MB", `href="/migrate/voltix-id" aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	f.migJob = jobs[2]
	_, body, _ = e.get(e.c, "/migrate/voltix-id?job=jmig000000000001")
	if !strings.Contains(body, `value="backup-x/homedir/public_html"`) || !strings.Contains(body, `name="op" value="choose"`) {
		t.Error("no folder choice")
	}
	f.migJob = jobs[4]
	_, body, _ = e.get(e.c, "/migrate/voltix-id?job=jmig000000000001")
	if !strings.Contains(body, "The site has moved") || !strings.Contains(body, "https://old.example.com → https://voltix-id.preview.nostek.dev") {
		t.Error("finished job not shown")
	}
	f.migJob = nil

	// plugin zip: what WordPress installs
	resp, err := e.c.Get(e.ts.URL + "/migrate-plugin/nostek-migrate.zip")
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil || resp.Header.Get("Content-Type") != "application/zip" || len(zr.File) != 1 || zr.File[0].Name != "nostek-migrate/nostek-migrate.php" {
		t.Fatalf("plugin zip: %v %v", err, resp.Header)
	}
	rc, _ := zr.File[0].Open()
	src, _ := io.ReadAll(rc)
	if !bytes.Contains(src, []byte("Plugin Name: Nostek Migrate")) {
		t.Error("zip lacks the plugin")
	}

	key := strings.Repeat("k", 43)
	code := "nm1." + base64.RawURLEncoding.EncodeToString([]byte(`{"u":"https://old.example.com","k":"`+key+`"}`))
	// without the confirmation nothing starts
	if c, _, h := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"plugin"}, "code": {code}}, "/migrate/voltix-id"); c != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Fatalf("no confirm: %d %v", c, h)
	}
	if c, _, h := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"plugin"}, "code": {code}, "confirm": {"1"}}, "/migrate/voltix-id"); c != 303 || h.Get("Location") != "/migrate/voltix-id?job=jmig000000000001#job" {
		t.Fatalf("plugin start: %d %v", c, h)
	}
	if c, _, h := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"link"}, "link": {"http://old.example.com/b.tar.gz"}, "confirm": {"1"}}, "/migrate/voltix-id"); c != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Fatalf("http link: %d %v", c, h)
	}
	if c, _, _ := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"link"}, "link": {"https://files.example.com/b.tar.gz?sig=SECRETSIG"}, "confirm": {"1"}}, "/migrate/voltix-id"); c != 303 {
		t.Fatalf("link: %d", c)
	}
	if c, _, _ := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"choose"}, "root": {"backup-x/homedir/public_html"}, "confirm": {"1"}}, "/migrate/voltix-id"); c != 303 {
		t.Fatalf("choose: %d", c)
	}
	if c, _, _ := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"clear"}}, "/migrate/voltix-id"); c != 303 {
		t.Fatalf("clear: %d", c)
	}
	if c, _, _ := e.post(e.c, "/migrate/voltix-id", url.Values{"op": {"ssh"}, "confirm": {"1"}}, "/migrate/voltix-id"); c != 400 {
		t.Fatalf("odd op: %d", c)
	}

	// upload: csrf first, confirmation, then the files streamed through
	_, page, _ := e.get(e.c, "/migrate/voltix-id")
	tok := csrfRe.FindStringSubmatch(page)[1]
	if c, _ := e.multipartPost(e.c, "/migrate/voltix-id/upload", [][2]string{{"@archive", "backup.tar.gz=TARGZ"}, {"csrf", tok}}); c != 403 {
		t.Fatalf("upload without csrf first: %d", c)
	}
	n := len(f.migCalls)
	if c, h := e.multipartPost(e.c, "/migrate/voltix-id/upload", [][2]string{{"csrf", tok}, {"@archive", "backup.tar.gz=TARGZ"}}); c != 303 || !strings.Contains(h.Get("Location"), "err=") || len(f.migCalls) != n {
		t.Fatalf("upload without confirm: %d %v", c, h)
	}
	if c, h := e.multipartPost(e.c, "/migrate/voltix-id/upload", [][2]string{{"csrf", tok}, {"confirm", "1"}, {"@archive", "backup.exe=MZ"}}); c != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Fatalf("odd archive: %d %v", c, h)
	}
	if c, h := e.multipartPost(e.c, "/migrate/voltix-id/upload", [][2]string{{"csrf", tok}, {"confirm", "1"}, {"@archive", "backup-home.tar.gz=TARGZ"}, {"@database", "cpuser_wp.sql.gz=SQLGZ"}}); c != 303 || h.Get("Location") != "/migrate/voltix-id?job=jmig000000000001#job" {
		t.Fatalf("upload: %d %v", c, h)
	}
	all := strings.Join(f.migCalls, "\n")
	for _, want := range []string{
		`POST  {"way":"plugin","code":"` + code + `"}`,
		`POST  {"way":"backup","link":"https://files.example.com/b.tar.gz?sig=SECRETSIG"}`,
		`POST  {"way":"backup","root":"backup-x/homedir/public_html"}`,
		"POST /clear",
		"PUT /upload?part=archive TARGZ",
		"PUT /upload?part=database SQLGZ",
		`POST  {"way":"backup"}`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("agent calls lack %q:\n%s", want, all)
		}
	}
	_, audit, _ := e.get(e.c, "/audit")
	for _, want := range []string{"migrate.start", "voltix-id plugin from old.example.com", "voltix-id backup link files.example.com", "migrate.upload", "archive backup-home.tar.gz, database cpuser_wp.sql.gz"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	if strings.Contains(audit, key) || strings.Contains(audit, "SECRETSIG") {
		t.Error("audit holds the migration key or the link's signature")
	}
}

func TestMigrateOnlyForWordPress(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	f.app = "static"
	e.setupAdmin(e.c, "admin@example.test")
	if code, _, _ := e.get(e.c, "/migrate/voltix-id"); code != 404 {
		t.Fatalf("static site migrate page: %d", code)
	}
	_, body, _ := e.get(e.c, "/files/voltix-id")
	if strings.Contains(body, `href="/migrate/voltix-id"`) {
		t.Error("migrate tab on a static site")
	}
}
