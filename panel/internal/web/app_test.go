package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestFormCreateApps(t *testing.T) {
	form := func(kv ...string) *http.Request {
		r, _ := http.NewRequest("POST", "/", nil)
		r.PostForm = url.Values{"domain": {" Voltix.ID "}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"}, "admin_email": {"o@nostek.dev"}, "title": {"Voltix"}}
		for i := 0; i+1 < len(kv); i += 2 {
			r.PostForm.Set(kv[i], kv[i+1])
		}
		return r
	}
	c, _ := formCreate(form())
	if c.App != "" || c.AdminEmail != "o@nostek.dev" || c.Database {
		t.Errorf("wordpress %+v", c)
	}
	c, _ = formCreate(form("app", "static", "database", "1"))
	if c.App != "static" || c.Server != "nginx" || c.PHP != "" || c.AdminEmail != "" || c.Database || c.Domain != "voltix.id" {
		t.Errorf("static %+v", c)
	}
	c, _ = formCreate(form("app", "php", "db_engine", "mariadb", "server", "nginx"))
	if c.App != "php" || c.Server != "nginx" || c.PHP != "8.4" || c.AdminEmail != "" || !c.Database || c.DBEngine != "mariadb" {
		t.Errorf("php %+v", c)
	}
	c, _ = formCreate(form("app", "php", "database", "1", "db_engine", "none"))
	if c.Database || c.DBEngine != "" {
		t.Error("database without choosing one")
	}
	c, _ = formCreate(form("app", "php", "db_engine", "postgres"))
	if !c.Database || c.DBEngine != "postgres" {
		t.Errorf("php on PostgreSQL %+v", c)
	}
	c, _ = formCreate(form("app", "laravel", "db_engine", "postgres", "laravel_db", "postgres"))
	if c.Database || c.DBEngine != "postgres" {
		t.Errorf("laravel on PostgreSQL %+v", c)
	}
	c, _ = formCreate(form("app", "laravel", "laravel_db", "mariadb"))
	if c.Database || c.DBEngine != "" {
		t.Errorf("laravel on MariaDB %+v", c)
	}
	c, _ = formCreate(form("laravel_db", "postgres", "db_engine", "postgres"))
	if c.Database || c.DBEngine != "" {
		t.Errorf("WordPress took an engine %+v", c)
	}
}

func TestAppSitePages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	for _, want := range []string{`name="app" value="wordpress" checked`, `name="app" value="php"`, `name="app" value="static"`, `name="db_engine" value="postgres"`, `name="laravel_db" value="postgres"`, "WordPress supports MySQL and MariaDB only"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	f.app = "static"
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/sites/voltix-id", "/files/voltix-id", "/backup/voltix-id"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d %v", p, lang, code, missingKey.FindString(body))
			}
			if strings.Contains(body, `href="/wp/voltix-id"`) || strings.Contains(body, `href="/db/voltix-id"`) {
				t.Errorf("%s: WordPress or Database tab on a static site", p)
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ = e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(body, "Static site") || strings.Contains(body, "wp-admin") || strings.Contains(body, `?logs=db"`) {
		t.Errorf("static overview: %v %v %v", strings.Contains(body, "Static site"), strings.Contains(body, "wp-admin"), strings.Contains(body, `?logs=db"`))
	}
	f.app, f.appDB = "php", true
	_, body, _ = e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(body, `href="/db/voltix-id"`) || strings.Contains(body, `href="/wp/voltix-id"`) || !strings.Contains(body, "MariaDB") || !strings.Contains(body, "PHP 8.4") {
		t.Error("php overview")
	}
	// the create request carries the app
	_, page, _ := e.get(e.c, "/sites/new")
	e.postWith(e.c, "/sites/new", url.Values{"step": {"create"}, "app": {"php"}, "db_engine": {"mariadb"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"nginx"}, "php": {"8.4"}, "admin_email": {"x@y.z"}}, page)
	if n := len(f.created); n == 0 || f.created[n-1].App != "php" || !f.created[n-1].Database || f.created[n-1].AdminEmail != "" {
		t.Fatalf("created %+v", f.created)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix.id php nginx") {
		t.Error("audit lacks the app")
	}
}

func TestDatabaseChoicePerApp(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	for _, c := range []struct {
		form         url.Values
		hidden, lead string
		database     bool
		engine       string
	}{
		{url.Values{"app": {"php"}, "server": {"apache"}, "php": {"8.4"}, "db_engine": {"postgres"}}, `name="db_engine" value="postgres"`, "PostgreSQL 18 database", true, "postgres"},
		{url.Values{"app": {"php"}, "server": {"nginx"}, "php": {"8.4"}, "db_engine": {"mariadb"}}, `name="db_engine" value="mariadb"`, "MariaDB database", true, "mariadb"},
		{url.Values{"app": {"laravel"}, "php": {"8.4"}, "laravel_db": {"postgres"}}, `name="laravel_db" value="postgres"`, "PostgreSQL 18, bukan MariaDB", false, "postgres"},
	} {
		c.form.Set("step", "plan")
		c.form.Set("domain", "voltix.id")
		c.form.Set("primary", "domain")
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"id"}})
		if c.form.Get("app") != "laravel" {
			e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
		}
		code, plan, _ := e.postWith(e.c, "/sites/new", c.form, body)
		if code != 200 || !strings.Contains(plan, c.hidden) || !strings.Contains(plan, c.lead) || missingKey.MatchString(plan) {
			t.Fatalf("%v plan %d %v", c.form, code, missingKey.FindString(plan))
		}
		c.form.Set("step", "create")
		e.postWith(e.c, "/sites/new", c.form, plan)
		got := f.created[len(f.created)-1]
		if got.Database != c.database || got.DBEngine != c.engine {
			t.Errorf("%v created %+v", c.form, got)
		}
	}
	f.app, f.appDB, f.dbEngine = "php", true, "postgres"
	if _, page, _ := e.get(e.c, "/sites/voltix-id"); !strings.Contains(page, ">PostgreSQL</span>") {
		t.Error("PHP overview lacks PostgreSQL")
	}
}
