package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func TestFormCreateNode(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.PostForm = url.Values{"app": {"node"}, "domain": {"shop.voltix.id"}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"},
		"admin_email": {"o@nostek.dev"}, "database": {"1"}, "version": {"13"}, "node_version": {" 24 "},
		"repo": {" https://github.com/owner/shop.git "}, "branch": {"main"}, "build": {"npm run build:prod"}, "start": {""}}
	c, _ := formCreate(r)
	if c.App != "node" || c.Server != "" || c.PHP != "" || c.AdminEmail != "" || c.Database || c.Version != "24" ||
		c.Repo != "https://github.com/owner/shop.git" || c.Branch != "main" || c.Build != "npm run build:prod" || c.Start != "" {
		t.Errorf("node %+v", c)
	}
	r.PostForm.Set("source", "files")
	r.PostForm.Set("dir", "/apps/web/")
	if c, _ = formCreate(r); c.Source != "files" || c.Repo != "" || c.Branch != "" || c.Dir != "apps/web" {
		t.Errorf("files source %+v", c)
	}
	r.PostForm.Set("app", "php")
	if c, _ = formCreate(r); c.Repo != "" || c.Build != "" || c.Version != "" || c.Source != "" || c.Dir != "" {
		t.Errorf("node fields on a PHP app %+v", c)
	}
}

func TestNodeServicesAndSteps(t *testing.T) {
	n := agent.Site{App: "node"}
	if !siteService(n, "app") || siteService(n, "web") || siteService(n, "php") || siteService(n, "db") || siteService(agent.Site{}, "app") {
		t.Error("node services")
	}
	rows, _ := jobProgress(agent.Job{Kind: "create", State: "running", Steps: []string{"files", "pull images", "fetch and build"}}, agent.Site{})
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "files,pull images,fetch and build,start new release" {
		t.Errorf("create steps %v", keys)
	}
	rows, _ = jobProgress(agent.Job{Kind: "deploy", State: "failed", Steps: []string{"fetch and build", "start new release", "previous release back"}}, agent.Site{})
	keys = nil
	for _, r := range rows {
		keys = append(keys, r.Key+"="+r.State)
	}
	if strings.Join(keys, ",") != "fetch and build=done,start new release=done,previous release back=failed" {
		t.Errorf("deploy steps %v", keys)
	}
}

func TestNodePages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	for _, want := range []string{`name="app" value="node"`, `name="repo"`, `name="branch" value="main"`, `<option value="24" selected>Node.js 24`, `name="start"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	// plan then create: the approve card carries the Node.js fields to the create step
	form := url.Values{"step": {"plan"}, "app": {"node"}, "domain": {"voltix.id"}, "primary": {"domain"}, "node_version": {"24"},
		"repo": {"https://github.com/owner/shop.git"}, "branch": {"main"}, "start": {"node server.js"}}
	f.app = "node"
	code, plan, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || !strings.Contains(plan, `name="repo" value="https://github.com/owner/shop.git"`) || !strings.Contains(plan, `name="node_version" value="24"`) ||
		!strings.Contains(plan, "<code>node server.js</code>") {
		t.Fatalf("plan %d", code)
	}
	form.Set("step", "create")
	e.postWith(e.c, "/sites/new", form, plan)
	if n := len(f.created); n == 0 || f.created[n-1].Repo != "https://github.com/owner/shop.git" || f.created[n-1].Version != "24" || f.created[n-1].Start != "node server.js" {
		t.Fatalf("created %+v", f.created)
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/sites/voltix-id", "/sites/voltix-id?logs=app", "/sites/voltix-id/deploys/2/log", "/sites/job/j0123456789abcdef"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d %v", p, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	for _, want := range []string{"Node.js 24", "Deploy now", "https://github.com/owner/shop.git", "release #3", "<code>0123456</code> Checkout &lt;b&gt;v2&lt;/b&gt;",
		`name="release" value="1"`, "build failed: see the build log", `href="/sites/voltix-id/deploys/2/log"`, "?logs=app"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	for _, bad := range []string{`name="release" value="3"`, `name="release" value="2"`, `href="/db/voltix-id"`, `href="/wp/voltix-id"`} {
		if strings.Contains(page, bad) {
			t.Errorf("overview holds %q", bad)
		}
	}
	_, list, _ := e.get(e.c, "/sites")
	if !strings.Contains(list, "Node.js 24</span>") || missingKey.MatchString(list) {
		t.Error("list lacks Node.js")
	}
	_, log, _ := e.get(e.c, "/sites/voltix-id/deploys/2/log")
	if !strings.Contains(log, "npm ERR! missing script: build &lt;script&gt;") {
		t.Error("build log not shown escaped")
	}
	if code, _, _ := e.get(e.c, "/sites/voltix-id/deploys/x/log"); code != 404 {
		t.Errorf("bad release %d", code)
	}
	code, _, loc := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"deploy"}}, page)
	if code != 303 || loc.Get("Location") != "/sites/job/j0123456789abcdef" || f.actions[len(f.actions)-1] != "deploy" {
		t.Errorf("deploy %d %v %s", code, f.actions, loc)
	}
	e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"rollback"}, "release": {"1"}}, page)
	if f.actions[len(f.actions)-1] != "deploys/1/rollback" {
		t.Errorf("rollback %v", f.actions)
	}
	for _, rel := range []string{"0", "x", ""} {
		if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"rollback"}, "release": {rel}}, page); code != 400 {
			t.Errorf("rollback %q answered %d", rel, code)
		}
	}
	if !strings.Contains(page, "<code>DATABASE_URL</code>") || !strings.Contains(page, `value="env-set"`) {
		t.Error("overview lacks environment values")
	}
	e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"env-set"}, "name": {"SECRET_X"}, "value": {"s3cr3t-value"}}, page)
	if a := f.actions[len(f.actions)-1]; a != `env {"delete":[],"set":{"SECRET_X":"s3cr3t-value"}}` {
		t.Errorf("env set %s", a)
	}
	e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"env-delete"}, "name": {"API_KEY"}}, page)
	if a := f.actions[len(f.actions)-1]; a != `env {"delete":["API_KEY"],"set":{}}` {
		t.Errorf("env delete %s", a)
	}
	if _, audit, _ := e.get(e.c, "/audit"); strings.Contains(audit, "s3cr3t-value") || !strings.Contains(audit, "SECRET_X") {
		t.Error("audit holds the value or lacks the name")
	}
	f.app = "php"
	if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"env-set"}, "name": {"A"}, "value": {"b"}}, page); code != 400 {
		t.Errorf("env on a PHP site answered %d", code)
	}
	if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"deploy"}}, page); code != 400 {
		t.Errorf("deploy on a PHP site answered %d", code)
	}
	if code, _, _ := e.get(e.c, "/sites/voltix-id/deploys/2/log"); code != 404 {
		t.Errorf("build log of a PHP site answered %d", code)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.deploy") || !strings.Contains(audit, "site.rollback") {
		t.Error("audit lacks deploy or rollback")
	}
}

func TestGoPythonPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	for _, want := range []string{`name="app" value="go"`, `name="app" value="python"`, `name="go_version"`, `<option value="1.27" selected>Go 1.27`, `<option value="3.14" selected>Python 3.14`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	for _, app := range []string{"go", "python"} {
		form := url.Values{"step": {"plan"}, "app": {app}, "domain": {"voltix.id"}, "primary": {"domain"}, "source": {"files"},
			"node_version": {"24"}, "go_version": {"1.27"}, "python_version": {"3.14"}}
		f.app = app
		code, plan, _ := e.postWith(e.c, "/sites/new", form, body)
		if code != 200 || !strings.Contains(plan, `name="`+app+`_version"`) || missingKey.MatchString(plan) {
			t.Fatalf("%s plan %d %v", app, code, missingKey.FindString(plan))
		}
		form.Set("step", "create")
		e.postWith(e.c, "/sites/new", form, plan)
		n := len(f.created)
		if want := map[string]string{"go": "1.27", "python": "3.14"}[app]; n == 0 || f.created[n-1].App != app || f.created[n-1].Version != want {
			t.Fatalf("%s created %+v", app, f.created)
		}
		for _, lang := range []string{"en", "id"} {
			e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
			for _, p := range []string{"/sites", "/sites/voltix-id"} {
				code, page, _ := e.get(e.c, p)
				if code != 200 || missingKey.MatchString(page) {
					t.Errorf("%s %s (%s): %d %v", app, p, lang, code, missingKey.FindString(page))
				}
			}
		}
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	}
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(page, "Python 1.27") || !strings.Contains(page, ".venv/bin/python app.py") {
		t.Error("python site overview")
	}
}

func TestTrashRemove(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.trash = []agent.Trashed{{Site: agent.Site{ID: "lab-nostek-dev", Domain: "lab.nostek.dev"}, Entry: "lab-nostek-dev.1700000000", Deleted: 1700000000, Purge: 1700604800}}
	_, page, _ := e.get(e.c, "/sites")
	if !strings.Contains(page, `action="/trash/lab-nostek-dev.1700000000/remove"`) || !strings.Contains(page, "Delete permanently") {
		t.Fatal("no delete permanently button in the trash")
	}
	if _, _, h := e.postWith(e.c, "/trash/lab-nostek-dev.1700000000/remove", url.Values{"confirm": {"lab.nostek.de"}}, page); len(f.removed) != 0 || h.Get("Location") != "/sites?done=remove_confirm#trash" {
		t.Fatalf("removed without the right domain: %v %q", f.removed, h.Get("Location"))
	}
	if code, _, _ := e.postWith(e.c, "/trash/other-site.1700000000/remove", url.Values{"confirm": {"lab.nostek.dev"}}, page); code != 404 || len(f.removed) != 0 {
		t.Fatalf("unknown entry: %d %v", code, f.removed)
	}
	if code, _, _ := e.postWith(e.c, "/trash/x/remove", url.Values{"confirm": {"lab.nostek.dev"}}, page); code != 404 {
		t.Fatalf("bad entry: %d", code)
	}
	if _, _, h := e.postWith(e.c, "/trash/lab-nostek-dev.1700000000/remove", url.Values{"confirm": {" Lab.Nostek.dev "}}, page); len(f.removed) != 1 || f.removed[0] != "lab-nostek-dev.1700000000" || h.Get("Location") != "/sites?done=removed" {
		t.Fatalf("remove: %v %q", f.removed, h.Get("Location"))
	}
	if e.postWith(e.c, "/trash/lab-nostek-dev.1700000000/remove", url.Values{"confirm": {"lab.nostek.dev"}}, ""); len(f.removed) != 1 {
		t.Fatalf("remove without CSRF token: %v", f.removed)
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, done := range []string{"removed", "remove_failed", "remove_confirm", "restore_failed"} {
			if code, page, _ := e.get(e.c, "/sites?done="+done); code != 200 || missingKey.MatchString(page) || !strings.Contains(page, map[bool]string{true: `class="banner ok"`, false: `class="banner warn"`}[done == "removed"]) {
				t.Errorf("%s %s: %d %v", lang, done, code, missingKey.FindString(page))
			}
		}
	}
	if _, page, _ := e.get(e.c, "/sites"); !strings.Contains(page, "Hapus permanen") {
		t.Error("id trash page")
	}
}

func TestPrivateRepoPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	form := url.Values{"step": {"plan"}, "app": {"node"}, "domain": {"voltix.id"}, "primary": {"domain"}, "node_version": {"24"},
		"repo": {"git@github.com:owner/private-shop.git"}, "branch": {"main"}}
	f.app = "node"
	code, plan, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || f.keys != 1 || !strings.Contains(plan, `name="key_id" value="0123456789abcdef"`) || !strings.Contains(plan, "AAAAITestKey") ||
		!strings.Contains(plan, "Deploy keys") || !strings.Contains(plan, "SHA256:testfp") || missingKey.MatchString(plan) {
		t.Fatalf("plan %d keys=%d %v", code, f.keys, missingKey.FindString(plan))
	}
	form.Set("step", "create")
	form.Set("key_id", "0123456789abcdef")
	e.postWith(e.c, "/sites/new", form, plan)
	if n := len(f.created); n == 0 || f.created[n-1].KeyID != "0123456789abcdef" || f.created[n-1].Repo != "git@github.com:owner/private-shop.git" {
		t.Fatalf("created %+v", f.created)
	}
	if f.keys != 1 {
		t.Errorf("create made another key: %d", f.keys)
	}
	// a public repository gets no key, and a key id sent with it is dropped
	form = url.Values{"step": {"plan"}, "app": {"node"}, "domain": {"voltix.id"}, "primary": {"domain"}, "node_version": {"24"},
		"repo": {"https://github.com/owner/shop.git"}, "branch": {"main"}, "key_id": {"0123456789abcdef"}}
	if _, plan, _ := e.postWith(e.c, "/sites/new", form, body); f.keys != 1 || strings.Contains(plan, `name="key_id"`) {
		t.Errorf("public repository got a key: %d", f.keys)
	}
	f.repo = "git@github.com:owner/private-shop.git"
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/sites/voltix-id")
		if code != 200 || !strings.Contains(page, "AAAAITestKey") || !strings.Contains(page, "SHA256:hostfp") || missingKey.MatchString(page) {
			t.Errorf("site page (%s) %d %v", lang, code, missingKey.FindString(page))
		}
	}
	f.repo = ""
	if _, page, _ := e.get(e.c, "/sites/voltix-id"); strings.Contains(page, "AAAAITestKey") {
		t.Error("key shown for a public repository")
	}
}

func TestAppDatabaseForm(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	r.PostForm = url.Values{"app": {"python"}, "domain": {"voltix.id"}, "source": {"files"}, "db_engine": {"none"}, "database": {"1"}}
	if c, _ := formCreate(r); c.Database || c.DBEngine != "" {
		t.Errorf("no database %+v", c)
	}
	r.PostForm.Set("db_engine", " PostgreSQL ")
	if c, _ := formCreate(r); !c.Database || c.DBEngine != "postgresql" { // the agent refuses an unknown engine
		t.Errorf("engine %+v", c)
	}
	r.PostForm.Set("db_engine", "postgres")
	if c, _ := formCreate(r); !c.Database || c.DBEngine != "postgres" {
		t.Errorf("postgres %+v", c)
	}
	r.PostForm.Set("app", "static")
	if c, _ := formCreate(r); c.Database || c.DBEngine != "" {
		t.Errorf("engine on a static site %+v", c)
	}
	pg := agent.Site{App: "go", Database: true, DBEngine: "postgres"}
	if !siteService(pg, "db") || !pg.IsPostgres() || pg.DBName() != "PostgreSQL" || siteService(agent.Site{App: "go"}, "db") {
		t.Error("go with PostgreSQL")
	}
	if my := (agent.Site{App: "node", Database: true}); !my.HasDB() || my.IsPostgres() || my.DBName() != "MariaDB" || (agent.Site{App: "go", DBEngine: "postgres"}).IsPostgres() {
		t.Error("node with MariaDB")
	}
}

func TestAppDatabasePages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	for _, want := range []string{`name="db_engine" value="none" checked`, `name="db_engine" value="postgres"`, "DATABASE_URL"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	f.app = "go"
	for _, engine := range []string{"mariadb", "postgres"} {
		form := url.Values{"step": {"plan"}, "app": {"go"}, "domain": {"voltix.id"}, "primary": {"domain"}, "source": {"files"}, "go_version": {"1.27"}, "db_engine": {engine}}
		code, plan, _ := e.postWith(e.c, "/sites/new", form, body)
		name := map[string]string{"mariadb": "MariaDB", "postgres": "PostgreSQL 18"}[engine]
		if code != 200 || !strings.Contains(plan, `name="db_engine" value="`+engine+`"`) || !strings.Contains(plan, name+" database") || missingKey.MatchString(plan) {
			t.Fatalf("%s plan %d %v", engine, code, missingKey.FindString(plan))
		}
		form.Set("step", "create")
		e.postWith(e.c, "/sites/new", form, plan)
		if n := len(f.created); n == 0 || !f.created[n-1].Database || f.created[n-1].DBEngine != engine {
			t.Fatalf("%s created %+v", engine, f.created)
		}
	}
	f.appDB, f.dbEngine = true, "postgres"
	if _, page, _ := e.get(e.c, "/sites/voltix-id"); !strings.Contains(page, ">PostgreSQL</span>") || !strings.Contains(page, "/db/voltix-id") {
		t.Error("site page lacks PostgreSQL")
	}
	if code, page, _ := e.get(e.c, "/db/voltix-id"); code != 200 || !strings.Contains(page, "PostgreSQL") || !strings.Contains(page, "FROM+%22") || !strings.Contains(page, "FROM pg_tables") || missingKey.MatchString(page) {
		t.Errorf("database page %d %v", code, missingKey.FindString(page))
	}
	f.dbEngine = ""
	if _, page, _ := e.get(e.c, "/db/voltix-id"); !strings.Contains(page, "FROM+%60") || strings.Contains(page, "PostgreSQL") {
		t.Error("MariaDB database page")
	}
}
