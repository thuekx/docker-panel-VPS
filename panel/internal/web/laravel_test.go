package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func TestFormCreateLaravel(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.PostForm = url.Values{"app": {"laravel"}, "domain": {"shop.voltix.id"}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"},
		"admin_email": {"o@nostek.dev"}, "database": {"1"}}
	c, _ := formCreate(r)
	if c.App != "laravel" || c.Server != "nginx" || c.PHP != "8.4" || c.AdminEmail != "" || c.Database || c.Version != "" {
		t.Errorf("laravel %+v", c)
	}
	r.PostForm.Set("version", " 12 ")
	if c, _ = formCreate(r); c.Version != "12" {
		t.Errorf("version %q", c.Version)
	}
	r.PostForm.Set("app", "php")
	if c, _ = formCreate(r); c.Version != "" {
		t.Errorf("version on a PHP app %q", c.Version)
	}
}

func TestSiteService(t *testing.T) {
	wp := agent.Site{Server: "nginx", PHP: "8.4"}
	static := agent.Site{Server: "nginx", App: "static"}
	php := agent.Site{Server: "apache", PHP: "8.4", App: "php"}
	lar := agent.Site{Server: "nginx", PHP: "8.4", App: "laravel"}
	for _, c := range []struct {
		s    agent.Site
		svc  string
		want bool
	}{
		{wp, "php", true}, {wp, "db", true}, {wp, "queue", false},
		{static, "php", false}, {static, "db", false}, {static, "web", true},
		{php, "php", false}, {php, "db", false},
		{lar, "php", true}, {lar, "db", true}, {lar, "queue", true}, {lar, "scheduler", true}, {lar, "cli", false}, {lar, "", false},
	} {
		if got := siteService(c.s, c.svc); got != c.want {
			t.Errorf("%s %q: %v", c.s.AppName(), c.svc, got)
		}
	}
}

func TestLaravelJobSteps(t *testing.T) {
	rows, _ := jobProgress(agent.Job{Kind: "create", State: "running", Steps: []string{"files", "build PHP image"}})
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "files,build PHP image,pull images,install Laravel,start containers" {
		t.Errorf("steps %v", keys)
	}
}

func TestLaravelPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	if !strings.Contains(body, `name="app" value="laravel"`) || !strings.Contains(body, "for-server") ||
		!strings.Contains(body, `<option value="13" selected>Laravel 13 · newest · needs PHP 8.3 or newer`) || !strings.Contains(body, `<option value="12" >Laravel 12`) {
		t.Error("form lacks Laravel")
	}
	f.app = "laravel"
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/sites/voltix-id", "/sites/voltix-id?logs=queue", "/db/voltix-id", "/backup/voltix-id"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d %v", p, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	for _, want := range []string{"Laravel 13</span>", "Run migrate", `value="optimize-clear"`, `href="/db/voltix-id"`, "?logs=scheduler", ".env"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(page, `href="/wp/voltix-id"`) {
		t.Error("WordPress tab on Laravel")
	}
	code, out, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"artisan"}, "op": {"migrate"}}, page)
	if code != 200 || !strings.Contains(out, "Nothing to migrate. &lt;b&gt;x&lt;/b&gt;") || !strings.Contains(out, "php artisan migrate") {
		t.Errorf("artisan result %d", code)
	}
	if n := len(f.actions); n == 0 || f.actions[n-1] != `artisan {"op":"migrate"}` {
		t.Errorf("agent got %v", f.actions)
	}
	for _, op := range []string{"db:wipe", "tinker", ""} {
		if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"artisan"}, "op": {op}}, page); code != 400 {
			t.Errorf("op %q answered %d", op, code)
		}
	}
	f.app = "php"
	if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"artisan"}, "op": {"migrate"}}, page); code != 400 {
		t.Errorf("artisan on a PHP site answered %d", code)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id migrate") {
		t.Error("audit lacks the artisan command")
	}
}
