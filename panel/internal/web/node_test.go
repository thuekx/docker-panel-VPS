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
		"admin_email": {"o@nostek.dev"}, "database": {"1"}, "version": {"13"}, "node": {" 24 "},
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
	rows, _ := jobProgress(agent.Job{Kind: "create", State: "running", Steps: []string{"files", "pull images", "fetch and build"}})
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "files,pull images,fetch and build,start new release" {
		t.Errorf("create steps %v", keys)
	}
	rows, _ = jobProgress(agent.Job{Kind: "deploy", State: "failed", Steps: []string{"fetch and build", "start new release", "previous release back"}})
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
	form := url.Values{"step": {"plan"}, "app": {"node"}, "domain": {"voltix.id"}, "primary": {"domain"}, "node": {"24"},
		"repo": {"https://github.com/owner/shop.git"}, "branch": {"main"}, "start": {"node server.js"}}
	f.app = "node"
	code, plan, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || !strings.Contains(plan, `name="repo" value="https://github.com/owner/shop.git"`) || !strings.Contains(plan, `name="node" value="24"`) ||
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
	f.app = "php"
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
