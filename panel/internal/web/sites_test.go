package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// fakeAgent answers like nostek-agent for the routes the panel uses.
type fakeAgent struct {
	mu        sync.Mutex
	created   []agent.Create
	secret    string
	deleted   []string
	actions   []string
	stackDown bool
	puts      []string // "path overwrite body" of every file write
	ops       []string // file operations
	sqls      []string
	imported  string
	wpCalls   []string // "path body" of every WordPress call
	wpDown    bool
	bakCalls  []string // "method path body" of every backup call
	bakDown   bool
	bakJob    agent.Job
	app       string // APP-01: app of the fake site (empty = WordPress)
	appDB     bool
	trash     []agent.Trashed
	removed   []string
	keys      int
	repo      string // APP-02b: repository of the fake runtime site
	dbEngine  string // APP-02b: engine of the fake site database
	hookOn    bool   // APP-02c
	hooks     []agent.Hook
	hookCalls []string
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	site := agent.Site{ID: "voltix-id", Domain: "voltix.id", Server: "nginx", PHP: "8.4", AdminUser: "admin-abc123",
		URL: "https://voltix-id.preview.nostek.dev", Phase: "ready", Status: "running",
		Hosts: []agent.Host{{Name: "voltix.id", Role: "primary"}, {Name: "voltix-id.preview.nostek.dev", Role: "preview", Routed: true}}}
	site.App, site.Database, site.DBEngine = f.app, f.appDB, f.dbEngine
	if f.app == "laravel" {
		site.Version = "13"
	}
	if f.app == "static" {
		site.PHP, site.AdminUser = "", ""
	}
	if f.app == "go" || f.app == "python" {
		site.PHP, site.Server, site.AdminUser, site.Version, site.Source, site.Release = "", "", "", "1.27", "files", 1
	}
	if f.app == "node" {
		site.PHP, site.Server, site.AdminUser, site.Version = "", "", "", "24"
		site.Repo, site.Branch, site.Start, site.Release = "https://github.com/owner/shop.git", "main", "node server.js", 3
		if f.repo != "" {
			site.Repo = f.repo
		}
	}
	write := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/files") || strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/db") {
		f.filesDB(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/backups") || strings.HasPrefix(r.URL.Path, "/v1/jobs/jbac") {
		f.backups(w, r, write)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/wp") {
		f.wordpress(w, r, write)
		return
	}
	switch {
	case r.URL.Path == "/v1/catalog":
		write(200, agent.Catalog{PHP: []agent.PHP{{Version: "8.1", EOL: true}, {Version: "8.4", Default: true, SecurityUntil: "2028-12-31"}},
			Laravel: []agent.Laravel{{Version: "13", MinPHP: "8.3", Default: true}, {Version: "12", MinPHP: "8.2"}},
			Node:    []agent.Node{{Version: "26", SecurityUntil: "2029-04-30"}, {Version: "24", SecurityUntil: "2028-04-30", Default: true}},
			Go:      []agent.Node{{Version: "1.27", SecurityUntil: "2027-08-31", Default: true}},
			Python:  []agent.Node{{Version: "3.14", SecurityUntil: "2030-10-31", Default: true}}})
	case r.URL.Path == "/v1/sites" && r.Method == "GET":
		write(200, map[string]any{"sites": []agent.Site{site}})
	case r.URL.Path == "/v1/trash":
		t := f.trash
		if t == nil {
			t = []agent.Trashed{}
		}
		write(200, map[string]any{"trash": t})
	case r.URL.Path == "/v1/deploykeys" && r.Method == "POST":
		f.keys++
		write(200, agent.DeployKey{ID: "0123456789abcdef", Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey nostek-panel-deploy-0123456789abcdef", Fingerprint: "SHA256:testfp"})
	case r.URL.Path == "/v1/sites/voltix-id/webhook":
		write(200, agent.Webhook{On: f.hookOn, Branch: "main", Deliveries: []agent.Delivery{{At: 1700000000, Event: "push", Result: "deploy", Commit: "0123456789abcdef0123456789abcdef01234567", Job: "j0123456789abcdef"}}})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/secret":
		f.hookOn = true
		f.hookCalls = append(f.hookCalls, "secret")
		write(200, map[string]string{"secret": "5ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2"})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/remove":
		f.hookOn = false
		f.hookCalls = append(f.hookCalls, "remove")
		write(200, map[string]any{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/webhook/deliver":
		var h agent.Hook
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &h)
		f.hooks = append(f.hooks, h)
		if h.Signature == "sha256=bad" {
			write(401, map[string]string{"error": "the delivery is not signed with this website's secret"})
			return
		}
		write(200, agent.Delivery{Event: h.Event, Result: "deploy", Job: "j0123456789abcdef"})
	case strings.HasPrefix(r.URL.Path, "/v1/sites/") && strings.HasSuffix(r.URL.Path, "/webhook/deliver"):
		write(404, map[string]string{"error": "auto-deploy is off for this website"})
	case r.URL.Path == "/v1/sites/voltix-id/deploykey":
		write(200, agent.DeployKey{Public: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey nostek-panel-deploy-0123456789abcdef", Fingerprint: "SHA256:testfp", HostKeys: []string{"ssh-ed25519 SHA256:hostfp"}})
	case strings.HasPrefix(r.URL.Path, "/v1/trash/") && strings.HasSuffix(r.URL.Path, "/remove"):
		f.removed = append(f.removed, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/trash/"), "/remove"))
		write(200, map[string]any{"ok": true})
	case r.URL.Path == "/v1/sites/plan", r.URL.Path == "/v1/sites" && r.Method == "POST":
		var c agent.Create
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &c)
		if c.Server != "apache" && c.Server != "nginx" && c.App != "node" && c.App != "go" && c.App != "python" {
			write(422, map[string]string{"error": "web server must be apache or nginx"})
			return
		}
		if r.URL.Path == "/v1/sites/plan" {
			site.App = c.App
			if site.IsNode() || c.App == "php" || c.App == "laravel" {
				site.Database, site.DBEngine = c.Database, c.DBEngine
			}
			write(200, site)
			return
		}
		f.created = append(f.created, c)
		write(202, map[string]any{"job": "j0123456789abcdef", "site": site})
	case r.URL.Path == "/v1/jobs/j0123456789abcdef":
		write(200, agent.Job{ID: "j0123456789abcdef", Site: "voltix-id", State: "done", Steps: []string{"files", "install WordPress"}, HasSecret: f.secret != ""})
	case r.URL.Path == "/v1/jobs/j0123456789abcdef/secret":
		if f.secret == "" {
			write(410, map[string]string{"error": "secret already shown"})
			return
		}
		s := f.secret
		f.secret = ""
		write(200, map[string]string{"password": s})
	case r.URL.Path == "/v1/sites/voltix-id/stack":
		if f.stackDown {
			write(502, map[string]string{"error": "cannot read containers: docker not reachable"})
			return
		}
		write(200, agent.Stack{Project: "np-site-voltix-id", Containers: []agent.Container{
			{Service: "web", Name: "np-site-voltix-id-web-1", Image: "nginx:1.30-alpine", State: "running", CPU: 0.4, MemBytes: 8 << 20, MemLimit: 256 << 20, Started: 1700000000 - 7200},
			{Service: "php", Name: "np-site-voltix-id-php-1", Image: "wordpress:7.1.4-php8.4-fpm", State: "exited", CPU: -1, MemBytes: -1, Restarts: 3},
			{Service: "db", Name: "np-site-voltix-id-db-1", Image: "mariadb:11.8", State: "running", Health: "unhealthy", CPU: 1.5, MemBytes: 120 << 20, MemLimit: 512 << 20, Started: 1700000000 - 600}}})
	case r.URL.Path == "/v1/sites/voltix-id" && r.Method == "GET":
		write(200, site)
	case r.URL.Path == "/v1/sites/voltix-id/delete":
		f.deleted = append(f.deleted, "voltix-id")
		write(200, map[string]any{"trash": "voltix-id.1700000000", "days": 7})
	case r.URL.Path == "/v1/sites/voltix-id/deploys" && r.Method == "GET":
		write(200, []agent.Deploy{
			{Release: 3, Started: 1700000000, Ended: 1700000095, Commit: "0123456789abcdef0123456789abcdef01234567", Message: "Checkout <b>v2</b>", Result: "live"},
			{Release: 2, Started: 1699990000, Ended: 1699990030, Result: "failed", Error: "build failed: see the build log"},
			{Release: 1, Started: 1699980000, Ended: 1699980090, Commit: "89abcdef0123456789abcdef0123456789abcdef", Message: "First", Result: "ok"}})
	case r.URL.Path == "/v1/sites/voltix-id/deploys" && r.Method == "POST":
		f.actions = append(f.actions, "deploy")
		write(202, map[string]string{"job": "j0123456789abcdef"})
	case r.URL.Path == "/v1/sites/voltix-id/deploys/2/log":
		write(200, map[string]string{"log": "npm ERR! missing script: build <script>"})
	case r.URL.Path == "/v1/sites/voltix-id/env" && r.Method == "GET":
		write(200, map[string][]string{"names": {"API_KEY", "DATABASE_URL"}})
	case r.URL.Path == "/v1/sites/voltix-id/env" && r.Method == "POST":
		b, _ := io.ReadAll(r.Body)
		f.actions = append(f.actions, "env "+string(b))
		write(200, map[string]bool{"ok": true})
	case r.URL.Path == "/v1/sites/voltix-id/laravel/artisan":
		b, _ := io.ReadAll(r.Body)
		f.actions = append(f.actions, "artisan "+string(b))
		if f.app != "laravel" {
			write(400, map[string]string{"error": "this website does not run Laravel"})
			return
		}
		write(200, map[string]string{"output": "INFO  Nothing to migrate. <b>x</b>"})
	case strings.HasPrefix(r.URL.Path, "/v1/sites/voltix-id/") && r.Method == "POST":
		f.actions = append(f.actions, strings.TrimPrefix(r.URL.Path, "/v1/sites/voltix-id/"))
		write(200, map[string]bool{"ok": true})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

func (e *env) withAgent() *fakeAgent {
	f := &fakeAgent{secret: "Pw-For-Test-Only-0123456"}
	ts := httptest.NewServer(f)
	e.t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	e.srv.SetAgent(agent.NewHTTP(&http.Client{Transport: rewrite{u}}))
	return f
}

type rewrite struct{ u *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host = r.u.Scheme, r.u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestSitesNeedLoginAndShowNoAgent(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/sites", "/sites/new", "/sites/voltix-id", "/sites/job/j0123456789abcdef"} {
		if code, _, h := e.get(e.c, p); code != 303 || h.Get("Location") != "/login" {
			t.Errorf("%s reachable without login: %d", p, code)
		}
	}
	e.setupAdmin(e.c, "admin@example.test")
	code, body, _ := e.get(e.c, "/sites")
	if code != 200 || !strings.Contains(body, "not reachable") || strings.Contains(body, "s-ok") {
		t.Fatalf("no agent must show a warning and never green: %d", code)
	}
}

func TestSiteCreateApproveAndPasswordOnce(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")

	_, body, _ := e.get(e.c, "/sites/new")
	if !strings.Contains(body, "admin@example.test") || !strings.Contains(body, "end of life") {
		t.Fatal("form lacks default e-mail or EOL label")
	}
	form := url.Values{"step": {"plan"}, "domain": {" Voltix.ID "}, "primary": {"domain"}, "server": {"nginx"}, "php": {"8.4"},
		"admin_email": {"admin@example.test"}, "title": {"Voltix"}, "aliases": {"voltix.co.id, VOLTIX.net"}}
	code, body, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || !strings.Contains(body, "Approve and create") || !strings.Contains(body, "waiting for DNS") {
		t.Fatalf("approve card: %d", code)
	}
	if len(f.created) != 0 {
		t.Fatal("plan step created a site")
	}
	form.Set("step", "create")
	code, _, h := e.postWith(e.c, "/sites/new", form, body)
	if code != 303 || h.Get("Location") != "/sites/job/j0123456789abcdef" {
		t.Fatalf("create: %d %s", code, h.Get("Location"))
	}
	c := f.created[0]
	if c.Domain != "voltix.id" || len(c.Aliases) != 2 || c.Aliases[1] != "voltix.net" {
		t.Fatalf("request sent = %+v", c)
	}

	_, body, _ = e.get(e.c, "/sites/job/j0123456789abcdef")
	if !strings.Contains(body, "Show admin password") || strings.Contains(body, "Pw-For-Test-Only") {
		t.Fatal("job page must offer the password without showing it")
	}
	// the starter sees the password exactly once
	_, body2, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites/job/j0123456789abcdef")
	if !strings.Contains(body2, "Pw-For-Test-Only-0123456") {
		t.Fatal("password not shown on the first request")
	}
	_, body3, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites/job/j0123456789abcdef")
	if strings.Contains(body3, "Pw-For-Test-Only") {
		t.Fatal("password shown twice")
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.create") || !strings.Contains(audit, "site.password_shown") || strings.Contains(audit, "Pw-For-Test-Only") {
		t.Fatal("audit wrong or holds the password")
	}
}

func TestSiteBadInputAndForeignJob(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	form := url.Values{"step": {"plan"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"caddy"}, "php": {"8.4"},
		"admin_email": {"admin@example.test"}, "title": {"Voltix"}}
	code, body, _ := e.post(e.c, "/sites/new", form, "/sites/new")
	if code != 422 || !strings.Contains(body, "apache or nginx") {
		t.Fatalf("bad server: %d", code)
	}
	// a job the user did not start: no password button and the secret route answers 404
	_, body, _ = e.get(e.c, "/sites/job/j0123456789abcdef")
	if strings.Contains(body, "Show admin password") {
		t.Fatal("foreign job offers the password")
	}
	if code, _, _ := e.post(e.c, "/sites/job/j0123456789abcdef/secret", url.Values{}, "/sites"); code != 404 {
		t.Fatalf("foreign secret: %d", code)
	}
	for _, p := range []string{"/sites/job/..%2fetc", "/sites/job/jXYZ", "/sites/UPPER", "/sites/a--b.c"} {
		if code, _, _ := e.get(e.c, p); code != 404 {
			t.Errorf("%s answered %d", p, code)
		}
	}
}

func TestSiteActionsAndDeleteConfirm(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	code, _, h := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"restart"}}, "/sites/voltix-id")
	if code != 303 || !strings.Contains(h.Get("Location"), "done=restart") || f.actions[0] != "restart" {
		t.Fatalf("restart: %d %s %v", code, h.Get("Location"), f.actions)
	}
	if code, _, _ := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"exec"}}, "/sites/voltix-id"); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
	e.post(e.c, "/sites/voltix-id", url.Values{"action": {"delete"}, "confirm": {"nostek.id"}}, "/sites/voltix-id")
	if len(f.deleted) != 0 {
		t.Fatal("delete ran with a wrong confirmation")
	}
	code, _, h = e.post(e.c, "/sites/voltix-id", url.Values{"action": {"delete"}, "confirm": {"Voltix.id"}}, "/sites/voltix-id")
	if code != 303 || h.Get("Location") != "/sites?done=delete" || len(f.deleted) != 1 {
		t.Fatalf("delete: %d %s", code, h.Get("Location"))
	}
	// POST without CSRF is refused
	resp, _ := e.c.PostForm(e.ts.URL+"/sites/voltix-id", url.Values{"action": {"stop"}})
	if resp.StatusCode != 403 {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	_, body, _ := e.get(e.c, "/sites")
	if !strings.Contains(body, "voltix.id") || !strings.Contains(body, "Running") {
		t.Fatal("list does not show the site")
	}
}

func TestSiteStackShownHonestly(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, _ := e.get(e.c, "/stack/voltix-id"); code != 303 {
		t.Fatal("stack JSON reachable without login")
	}
	e.setupAdmin(e.c, "admin@example.test")
	code, body, _ := e.get(e.c, "/sites/voltix-id")
	if code != 200 || !strings.Contains(body, "np-site-voltix-id-php-1") || !strings.Contains(body, "1/3 running") {
		t.Fatalf("site page: %d", code)
	}
	// php stopped and db unhealthy are never green; only web is
	if strings.Count(body, `class="pill s-ok" data-f="state"`) != 1 || !strings.Contains(body, `class="pill s-bad" data-f="state">Stopped`) || !strings.Contains(body, "Running · unhealthy") {
		t.Fatal("container states not shown honestly")
	}
	if !strings.Contains(body, "8 MB / 256 MB") || !strings.Contains(body, "2h 0m") {
		t.Fatal("usage or uptime missing")
	}
	code, js, h := e.get(e.c, "/stack/voltix-id")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(js, `"mem_pct":23`) {
		t.Fatalf("stack JSON: %d %s", code, js)
	}
	_, list, _ := e.get(e.c, "/sites")
	if !strings.Contains(list, "Containers") {
		t.Fatal("list lacks the containers column")
	}
	// restart one container: only known services, audited
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	for _, bad := range []string{"cli", "edge", ""} {
		if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"restart-service"}, "service": {bad}}, page); code != 400 {
			t.Errorf("service %q accepted: %d", bad, code)
		}
	}
	code, _, h = e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"restart-service"}, "service": {"php"}}, page)
	if code != 303 || h.Get("Location") != "/sites/voltix-id?done=restart_php" {
		t.Fatalf("restart php: %d %s", code, h.Get("Location"))
	}
	if f.actions[len(f.actions)-1] != "services/php/restart" {
		t.Fatalf("agent call: %v", f.actions)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.restart_php") {
		t.Fatal("restart not audited")
	}
	// agent cannot read docker: unknown, never green
	f.stackDown = true
	_, body, _ = e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(body, "cannot be read") || strings.Contains(body, `data-stack-count`) {
		t.Fatal("docker failure must show unknown")
	}
	if code, _, _ := e.get(e.c, "/stack/voltix-id"); code != 502 {
		t.Fatalf("stack JSON on failure: %d", code)
	}
}
