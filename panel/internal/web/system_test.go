package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) system(r *http.Request, write func(int, any)) {
	p := r.URL.Path
	if r.Method == "POST" {
		f.sysCalls = append(f.sysCalls, p)
	}
	switch {
	case p == "/v1/server/services":
		write(200, map[string]any{"services": []agent.CoreService{
			{Name: "edge", Kind: "container", State: "running", OK: true, Detail: "Up 3 hours (healthy)", Restart: true},
			{Name: "panel", Kind: "container", State: "running", OK: true, Restart: true},
			{Name: "waf", Kind: "container", State: "exited", Detail: "Exited (1)", Restart: true},
			{Name: "docker-api", Kind: "container", State: "running", OK: true, Restart: true},
			{Name: "sftp", Kind: "container", State: "running", OK: true, Restart: true},
			{Name: "docker", Kind: "unit", State: "active", OK: true, Detail: "docker.service"},
			{Name: "agent", Kind: "unit", State: "active", OK: true, Restart: true},
			{Name: "fail2ban", Kind: "unit", State: "active", OK: true, Restart: true},
			{Name: "nginx", Kind: "unit", State: "failed", Restart: true},
			{Name: "mariadb", Kind: "unit", State: "active", OK: true, Restart: true},
		}})
	case p == "/v1/server/processes":
		write(200, agent.ProcView{Cores: 2, MemTotal: 4 << 30, Count: 120,
			ByCPU: []agent.Proc{{PID: 812, Name: "php-fpm", User: "www-data", CPU: 37.5, RSS: 90 << 20, Mem: 2.2, Where: "np-site-voltix-id-php-1"}},
			ByMem: []agent.Proc{{PID: 640, Name: "mariadbd", User: "mysql", CPU: 1, RSS: 400 << 20, Mem: 9.8, Where: "mariadb.service"}}})
	case p == "/v1/server/drift":
		write(200, map[string]any{"sites": []agent.DriftSite{
			{Site: "voltix-id", Domain: "voltix.id", Drift: true, Files: []agent.DriftFile{{File: "compose.yml", State: "same"}, {File: "php.ini", State: "changed", Added: 1, Removed: 1, Diff: "- memory_limit = 1024M\n+ memory_limit = 256M"}}},
			{Site: "shop-id", Domain: "shop.id", Native: true},
			{Site: "blog-id", Domain: "blog.id", Files: []agent.DriftFile{{File: "compose.yml", State: "missing"}}, Drift: true},
		}})
	case strings.Contains(p, "/services/waf/restart"):
		write(500, map[string]string{"error": "restart nostek-core-waf-1 failed: boom"})
	default:
		write(200, map[string]bool{"ok": true})
	}
}

func TestSystemPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/settings/system"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?done=restart", "?done=restart-agent", "?done=drift", "?err=x"} {
			code, body, _ := e.get(e.c, "/settings/system"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/system"`) || missingKey.MatchString(body) {
			t.Errorf("settings (%s) lacks the system card", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/system")
	for _, want := range []string{"2 service(s) are not running well", "php-fpm", "37.5%", "np-site-voltix-id-php-1", "mariadbd", "400.0 MB",
		"2 website(s) differ", "- memory_limit = 1024M", "native: not compared", "from SSH only", `value="drift-fix"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Count(body, `name="op" value="drift-fix"`) != 2 {
		t.Error("write back offered for a website without drift")
	}
	post := func(v url.Values) (int, string) {
		code, _, h := e.post(e.c, "/settings/system", v, "/settings/system")
		return code, h.Get("Location")
	}
	if _, loc := post(url.Values{"op": {"restart"}, "name": {"edge"}}); loc != "/settings/system?done=restart#services" {
		t.Fatal(loc)
	}
	if _, loc := post(url.Values{"op": {"restart"}, "name": {"agent"}}); loc != "/settings/system?done=restart-agent#services" {
		t.Fatal(loc)
	}
	if _, loc := post(url.Values{"op": {"restart"}, "name": {"waf"}}); !strings.Contains(loc, "err=") {
		t.Fatal(loc)
	}
	if code, _ := post(url.Values{"op": {"restart"}, "name": {"../x"}}); code != 400 {
		t.Fatal(code)
	}
	if _, loc := post(url.Values{"op": {"drift-fix"}, "site": {"voltix-id"}}); loc != "/settings/system?done=drift#drift" {
		t.Fatal(loc)
	}
	if code, _ := post(url.Values{"op": {"drift-fix"}, "site": {"Bad ID"}}); code != 400 {
		t.Fatal(code)
	}
	if got := strings.Join(f.sysCalls, " "); got != "/v1/server/services/edge/restart /v1/server/services/agent/restart /v1/server/services/waf/restart /v1/sites/voltix-id/drift/fix" {
		t.Fatal(got)
	}
	audit := e.auditText()
	for _, want := range []string{"server.restart edge", "server.restart waf failed", "server.drift.fix voltix-id"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}
