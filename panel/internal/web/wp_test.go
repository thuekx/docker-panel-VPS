package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const loginLink = "https://voltix-id.preview.nostek.dev/?nostek_login=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// wordpress answers the WordPress Tools routes of the fake agent.
func (f *fakeAgent) wordpress(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	p := strings.TrimPrefix(r.URL.Path, "/v1/sites/voltix-id/wp")
	b, _ := io.ReadAll(r.Body)
	if r.Method == "POST" {
		f.wpCalls = append(f.wpCalls, strings.TrimSpace(p+" "+string(b)))
	}
	switch p {
	case "":
		if f.wpDown {
			write(400, map[string]string{"error": "WordPress cannot reach its database; start the website first"})
			return
		}
		write(200, agent.WP{Version: "6.9", CoreUpdate: "6.9.1", Title: "Voltix <b>", URL: "https://voltix-id.preview.nostek.dev",
			Admins: []string{"admin-abc123", "ops"}, AdminUser: "admin-abc123", Indexing: true, Debug: false, FileEditOff: true, SSLAdmin: true,
			PHP: "8.4", Server: "nginx", PluginUpdates: 1,
			Plugins: []agent.WPItem{{Name: "akismet", Title: "Akismet", Status: "active", Version: "5.3", Update: "available", UpdateVersion: "5.4", AutoUpdate: "off"},
				{Name: "nostek-panel", Title: "Nostek Panel", Status: "must-use", Update: "none"}},
			Themes: []agent.WPItem{{Name: "twentytwentyfive", Title: "Twenty Twenty-Five", Status: "active", Version: "1.3", Update: "none", AutoUpdate: "on"},
				{Name: "twentytwentyfour", Title: "Twenty Twenty-Four", Status: "inactive", Version: "1.2", Update: "none", AutoUpdate: "off"}},
			TLS:      agent.TLS{Host: "voltix-id.preview.nostek.dev", OK: true, Issuer: "R12", Expires: 1900000000},
			Warnings: []string{}})
	case "/login":
		write(200, map[string]string{"url": loginLink, "user": "admin-abc123"})
	case "/plugins", "/themes", "/core":
		if strings.Contains(string(b), "broken") {
			write(400, map[string]string{"error": "plugin broken could not be found"})
			return
		}
		write(200, map[string]string{"output": "Success: Updated 1 of 1 plugins."})
	case "/settings":
		write(200, map[string]bool{"ok": true})
	case "/check":
		write(200, agent.Check{CoreOK: true, Core: "Success: WordPress installation verifies against checksums.", PluginsOK: false, Plugins: "Warning: could not retrieve checksums for premium-x"})
	case "/cli":
		write(200, agent.CLIResult{Code: 0, Output: "6.9 <script>"})
	default:
		write(404, map[string]string{"error": "not found"})
	}
}

func TestWPPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/wp/voltix-id"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("WordPress page reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, tab := range []string{"dashboard", "plugins", "themes", "cli", "bogus"} {
			code, body, _ := e.get(e.c, "/wp/voltix-id?tab="+tab)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", tab, lang, code, missingKey.FindString(body))
			}
			if strings.Contains(body, "Voltix <b>") {
				t.Fatal("site title not escaped")
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/wp/voltix-id")
	for _, want := range []string{"Update to 6.9.1", "HTTPS certificate valid", "Take over wp-cron.php", "Maintenance mode", `href="/wp/voltix-id"`, "Log in to wp-admin"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	_, body, _ = e.get(e.c, "/wp/voltix-id?tab=plugins")
	if !strings.Contains(body, "Akismet") || !strings.Contains(body, "→ 5.4") || !strings.Contains(body, "managed by the system") {
		t.Error("plugins tab incomplete")
	}
	f.wpDown = true
	if code, body, _ := e.get(e.c, "/wp/voltix-id"); code != 200 || !strings.Contains(body, "start the website first") {
		t.Errorf("agent error not shown: %d", code)
	}
	if code, _, _ := e.get(e.c, "/wp/Bad_Id"); code != 404 {
		t.Errorf("bad id answered %d", code)
	}
}

func TestWPActions(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, page, _ := e.get(e.c, "/wp/voltix-id")
	if code, _, _ := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"set"}, "key": {"debug"}, "on": {"1"}}, ""); code != 403 {
		t.Fatalf("no CSRF answered %d", code)
	}
	// one-click login: a page that follows the link (form-action stays 'self'), never a cross-site redirect
	code, body, h := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"login"}}, page)
	if code != 200 || !strings.Contains(body, `href="`+loginLink+`"`) || !strings.Contains(body, "data-go") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("login page: %d %v", code, h)
	}
	steps := []struct {
		form url.Values
		code int
		want string
	}{
		{url.Values{"op": {"set"}, "key": {"system_cron"}, "on": {"1"}}, 303, `/settings {"key":"system_cron","on":true}`},
		{url.Values{"op": {"set"}, "key": {"maintenance"}, "on": {"0"}}, 303, `/settings {"key":"maintenance","on":false}`},
		{url.Values{"op": {"core"}, "do": {"all"}}, 200, `/core {"do":"all"}`},
		{url.Values{"op": {"plugin"}, "do": {"update"}, "all": {"1"}}, 200, `/plugins {"do":"update","all":true}`},
		{url.Values{"op": {"plugin"}, "do": {"install"}, "name": {"wordfence"}, "activate": {"1"}}, 200, `/plugins {"do":"install","names":["wordfence"],"activate":true}`},
		{url.Values{"op": {"theme"}, "do": {"delete"}, "name": {"twentytwentyfour"}}, 200, `/themes {"do":"delete","names":["twentytwentyfour"]}`},
		{url.Values{"op": {"plugin"}, "do": {"delete"}, "name": {"broken"}}, 422, `/plugins {"do":"delete","names":["broken"]}`},
		{url.Values{"op": {"check"}}, 200, "/check {}"},
		{url.Values{"op": {"cli"}, "line": {"wp user update 1 --user_pass=topsecret-xyz"}}, 200, `/cli {"line":"wp user update 1 --user_pass=topsecret-xyz"}`},
	}
	for _, s := range steps {
		code, body, _ := e.postWith(e.c, "/wp/voltix-id", s.form, page)
		if code != s.code {
			t.Errorf("%v: %d", s.form, code)
		}
		if got := f.wpCalls[len(f.wpCalls)-1]; got != s.want {
			t.Errorf("agent got %q, want %q", got, s.want)
		}
		if strings.Contains(body, "<script>") {
			t.Error("console output not escaped")
		}
	}
	n := len(f.wpCalls)
	for _, bad := range []url.Values{
		{"op": {"set"}, "key": {"siteurl"}, "on": {"1"}},
		{"op": {"core"}, "do": {"rollback"}},
		{"op": {"plugin"}, "do": {"eval"}, "name": {"x"}},
		{"op": {"shell"}},
	} {
		if code, _, _ := e.postWith(e.c, "/wp/voltix-id", bad, page); code != 400 {
			t.Errorf("%v answered %d", bad, code)
		}
	}
	if code, _, _ := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"plugin"}, "do": {"activate"}, "name": {"--require=/x"}}, page); code != 422 {
		t.Errorf("flag as plugin name answered %d", code)
	}
	if len(f.wpCalls) != n {
		t.Error("refused input reached the agent")
	}
	_, audit, _ := e.get(e.c, "/audit")
	if strings.Contains(audit, "topsecret-xyz") || strings.Contains(audit, "nostek_login=") {
		t.Error("audit holds a console argument or the login link")
	}
	if !strings.Contains(audit, "wp.cli") || !strings.Contains(audit, "wp.login") {
		t.Error("audit lacks WordPress actions")
	}
}
