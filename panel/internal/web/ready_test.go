package web

import (
	"net/http"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"golang.org/x/crypto/bcrypt"
	"net/url"
	"strings"
	"testing"
)

func TestActionsShowProgress(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, body, _ := e.get(e.c, "/sites/voltix-id")
		for _, a := range []string{"start", "restart", "stop"} {
			if !strings.Contains(body, `value="`+a+`" type="submit" data-busy="`) {
				t.Errorf("%s (%s): no progress on the button", a, lang)
			}
		}
		if !strings.Contains(body, `class="muted busy-note" role="status" hidden`) || strings.Contains(body, "[sites.busy") {
			t.Errorf("(%s) no progress note", lang)
		}
	}
}

func TestReadyApps(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.PostForm = url.Values{"app": {"n8n"}, "domain": {"flow.nostek.dev"}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"}, "admin_email": {"o@nostek.dev"}, "db_engine": {"postgres"}, "version": {"13"}}
	if c, _ := formCreate(r); c.App != "n8n" || c.Server != "" || c.PHP != "" || c.AdminEmail != "" || c.Database || c.DBEngine != "" || c.Version != "" {
		t.Errorf("hidden fields reach the agent: %+v", c)
	}
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, body, _ := e.get(e.c, "/sites/new")
		if !strings.Contains(body, `name="app" value="uptime-kuma" data-ready`) || !strings.Contains(body, `value="n8n" data-ready`) || !strings.Contains(body, "2.47.7") || strings.Contains(body, "[ready.") || strings.Contains(body, "[sites.app.") {
			t.Fatalf("form (%s) lacks the ready apps", lang)
		}
		_, page, _ := e.get(e.c, "/sites/new")
		code, plan, _ := e.postWith(e.c, "/sites/new", url.Values{"step": {"plan"}, "app": {"uptime-kuma"}, "domain": {"status.nostek.dev"}, "primary": {"domain"}}, page)
		if code != 200 || !strings.Contains(plan, `name="app" value="uptime-kuma"`) || missingKey.MatchString(plan) || strings.Contains(plan, "[sites.") {
			t.Fatalf("approve card (%s) %d %v", lang, code, missingKey.FindString(plan))
		}
	}
	for _, app := range []string{"uptime-kuma", "n8n", "ghost"} {
		f.app = app
		for _, lang := range []string{"en", "id"} {
			e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
			code, body, _ := e.get(e.c, "/sites/voltix-id")
			if code != 200 || !strings.Contains(body, `?logs=app"`) || strings.Contains(body, `?logs=web"`) || strings.Contains(body, `id="deploy"`) || strings.Contains(body, "[sites.") || missingKey.MatchString(body) {
				t.Errorf("%s overview (%s) %d %v", app, lang, code, missingKey.FindString(body))
			}
			_, job, _ := e.get(e.c, "/sites/job/j0123456789abcdef")
			if !strings.Contains(job, `href="https://voltix-id.preview.nostek.dev" target="_blank"`) || strings.Contains(job, "[ready.") || strings.Contains(job, "wp-admin/") {
				t.Errorf("%s job page (%s)", app, lang)
			}
			_, list, _ := e.get(e.c, "/sites")
			if strings.Contains(list, "[sites.") {
				t.Errorf("%s list (%s)", app, lang)
			}
		}
	}
	// Ghost (APP-03 wave 2): its own MySQL 8 is named on the approve card, nothing to choose
	f.app = ""
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, page, _ := e.get(e.c, "/sites/new")
		if !strings.Contains(page, `value="ghost" data-ready`) || !strings.Contains(page, "6.79.0-alpine") {
			t.Fatalf("form (%s) lacks Ghost", lang)
		}
		_, plan, _ := e.postWith(e.c, "/sites/new", url.Values{"step": {"plan"}, "app": {"ghost"}, "domain": {"blog.nostek.dev"}, "primary": {"domain"}, "db_engine": {"postgres"}}, page)
		if !strings.Contains(plan, "MySQL 8") || missingKey.MatchString(plan) || strings.Contains(plan, "[sites.") {
			t.Errorf("ghost approve card (%s) %v", lang, missingKey.FindString(plan))
		}
	}
	r.PostForm = url.Values{"app": {"ghost"}, "domain": {"blog.nostek.dev"}, "primary": {"domain"}, "db_engine": {"mariadb"}}
	if c, _ := formCreate(r); c.App != "ghost" || c.Database || c.DBEngine != "" {
		t.Errorf("ghost create %+v", c)
	}
	_, page, _ := e.get(e.c, "/sites/new")
	e.postWith(e.c, "/sites/new", url.Values{"step": {"create"}, "app": {"n8n"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"}}, page)
	if n := len(f.created); n == 0 || f.created[n-1].App != "n8n" || f.created[n-1].Server != "" || f.created[n-1].PHP != "" {
		t.Fatalf("created %+v", f.created)
	}
}

// APP-03 wave 3: a new ready app gets a setup login (only its hash reaches the agent), shown once to its creator,
// and the job page offers "Open to everyone" while the lock is on.
func TestReadySetupLock(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, page, _ := e.get(e.c, "/sites/new")
		_, plan, _ := e.postWith(e.c, "/sites/new", url.Values{"step": {"plan"}, "app": {"ghost"}, "domain": {"blog.nostek.dev"}, "primary": {"domain"}}, page)
		if !strings.Contains(plan, `name="setup_lock" value="1" checked`) || missingKey.MatchString(plan) {
			t.Errorf("approve card (%s) lacks the lock %v", lang, missingKey.FindString(plan))
		}
		_, plan, _ = e.postWith(e.c, "/sites/new", url.Values{"step": {"plan"}, "app": {"static"}, "domain": {"s.nostek.dev"}, "primary": {"domain"}, "server": {"nginx"}}, page)
		if strings.Contains(plan, `name="setup_lock"`) {
			t.Errorf("static site offers a setup lock")
		}
	}
	_, page, _ := e.get(e.c, "/sites/new")
	e.postWith(e.c, "/sites/new", url.Values{"step": {"create"}, "app": {"uptime-kuma"}, "domain": {"voltix.id"}, "primary": {"domain"}, "setup_lock": {"1", "0"}}, page)
	c := f.created[len(f.created)-1]
	if c.SetupLock == nil || c.SetupLock.User != "setup" || !strings.HasPrefix(c.SetupLock.Hash, "$2a$") {
		t.Fatalf("lock %+v", c.SetupLock)
	}
	f.app, f.protected = "uptime-kuma", []agent.ProtectedDir{{Path: "/", Users: []string{"setup"}}}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, job, _ := e.get(e.c, "/sites/job/j0123456789abcdef")
		if !strings.Contains(job, `action="/sites/job/j0123456789abcdef/setup-lock"`) || !strings.Contains(job, `name="op" value="protect-remove"`) ||
			!strings.Contains(job, `name="path" value="/"`) || missingKey.MatchString(job) {
			t.Errorf("job page (%s) %v", lang, missingKey.FindString(job))
		}
	}
	code, shown, _ := e.postWith(e.c, "/sites/job/j0123456789abcdef/setup-lock", url.Values{}, page)
	pw := ""
	if i := strings.Index(shown, `<code id="pw">`); i >= 0 {
		pw = shown[i+len(`<code id="pw">`):]
		pw = pw[:strings.Index(pw, "<")]
	}
	if code != 200 || len(pw) != 20 || bcrypt.CompareHashAndPassword([]byte(c.SetupLock.Hash), []byte(pw)) != nil {
		t.Fatalf("shown %d %q", code, pw)
	}
	if code, _, _ := e.postWith(e.c, "/sites/job/j0123456789abcdef/setup-lock", url.Values{}, page); code != 410 {
		t.Errorf("second view %d", code)
	}
	if _, job, _ := e.get(e.c, "/sites/job/j0123456789abcdef"); strings.Contains(job, "/setup-lock") || strings.Contains(job, pw) {
		t.Errorf("password offered again")
	}
	f.protected = nil
	if _, job, _ := e.get(e.c, "/sites/job/j0123456789abcdef"); strings.Contains(job, "protect-remove") {
		t.Errorf("open button without a lock")
	}
	e.postWith(e.c, "/sites/new", url.Values{"step": {"create"}, "app": {"n8n"}, "domain": {"voltix.id"}, "primary": {"domain"}, "setup_lock": {"0"}}, page)
	e.postWith(e.c, "/sites/new", url.Values{"step": {"create"}, "app": {"php"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"nginx"}, "php": {"8.4"}, "setup_lock": {"1"}}, page)
	for _, c := range f.created[len(f.created)-2:] {
		if c.SetupLock != nil {
			t.Errorf("%s got a lock", c.App)
		}
	}
}
