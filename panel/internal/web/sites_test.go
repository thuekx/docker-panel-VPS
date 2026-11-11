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
	mu      sync.Mutex
	created []agent.Create
	secret  string
	deleted []string
	actions []string
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	site := agent.Site{ID: "voltix-id", Domain: "voltix.id", Server: "nginx", PHP: "8.4", AdminUser: "admin-abc123",
		URL: "https://voltix-id.preview.nostek.dev", Phase: "ready", Status: "running",
		Hosts: []agent.Host{{Name: "voltix.id", Role: "primary"}, {Name: "voltix-id.preview.nostek.dev", Role: "preview", Routed: true}}}
	write := func(code int, v any) { w.WriteHeader(code); json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/v1/catalog":
		write(200, agent.Catalog{PHP: []agent.PHP{{Version: "8.1", EOL: true}, {Version: "8.4", Default: true, SecurityUntil: "2028-12-31"}}})
	case r.URL.Path == "/v1/sites" && r.Method == "GET":
		write(200, map[string]any{"sites": []agent.Site{site}})
	case r.URL.Path == "/v1/trash":
		write(200, map[string]any{"trash": []agent.Trashed{}})
	case r.URL.Path == "/v1/sites/plan", r.URL.Path == "/v1/sites" && r.Method == "POST":
		var c agent.Create
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &c)
		if c.Server != "apache" && c.Server != "nginx" {
			write(422, map[string]string{"error": "web server must be apache or nginx"})
			return
		}
		if r.URL.Path == "/v1/sites/plan" {
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
	case r.URL.Path == "/v1/sites/voltix-id" && r.Method == "GET":
		write(200, site)
	case r.URL.Path == "/v1/sites/voltix-id/delete":
		f.deleted = append(f.deleted, "voltix-id")
		write(200, map[string]any{"trash": "voltix-id.1700000000", "days": 7})
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
