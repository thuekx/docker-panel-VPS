package web

import (
	"net/http"
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
