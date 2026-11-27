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
	if strings.HasPrefix(r.URL.Path, "/v1/jobs/jc0de") {
		write(200, f.wpJob)
		return
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
			Warnings: []string{"plugins: WP-CLI failed: [{\"name\":\"x\"}]"}})
	case "/login":
		write(200, map[string]string{"url": loginLink, "user": "admin-abc123"})
	case "/plugins", "/themes", "/core":
		if strings.Contains(string(b), "broken") {
			write(400, map[string]string{"error": "plugin broken could not be found"})
			return
		}
		if strings.Contains(string(b), `"safe":true`) {
			write(202, map[string]string{"job": "jc0de000000000001"})
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
	if strings.Index(body, "plugin list could not be read") < strings.Index(body, "Maintenance mode") || !strings.Contains(body, "Technical details") {
		t.Error("warnings must sit at the bottom, explained in plain words")
	}
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
		{url.Values{"op": {"core"}, "do": {"all"}, "safe": {"0"}}, 200, `/core {"do":"all"}`},
		{url.Values{"op": {"plugin"}, "do": {"update"}, "all": {"1"}, "safe": {"0"}}, 200, `/plugins {"do":"update","all":true}`},
		{url.Values{"op": {"core"}, "do": {"all"}}, 303, `/core {"do":"all","safe":true}`},
		{url.Values{"op": {"plugin"}, "do": {"update"}, "name": {"akismet"}}, 303, `/plugins {"do":"update","names":["akismet"],"safe":true}`},
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

func TestWPSafeUpdate(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, page, _ := e.get(e.c, "/wp/voltix-id")
	if !strings.Contains(page, "makes a backup first") {
		t.Error("updates card does not say an update is safe")
	}
	code, _, h := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"plugin"}, "do": {"update"}, "all": {"1"}}, page)
	if code != 303 || h.Get("Location") != "/wp/voltix-id?tab=plugins&job=jc0de000000000001#job" {
		t.Fatalf("safe update: %d %q", code, h.Get("Location"))
	}
	if got := f.wpCalls[len(f.wpCalls)-1]; got != `/plugins {"do":"update","all":true,"safe":true}` {
		t.Fatalf("agent got %q", got)
	}
	cases := []struct {
		job  agent.Job
		want []string
		not  []string
	}{
		{agent.Job{ID: "jc0de000000000001", Kind: "wp-update", Site: "voltix-id", State: "running", Steps: []string{"check before", "copy database"}},
			[]string{`data-job="/sites/job/jc0de000000000001/status"`, "Backing up the database…", `data-step="check after"`}, []string{`data-step="restore"`}},
		{agent.Job{ID: "jc0de000000000001", Kind: "wp-update", Site: "voltix-id", State: "done", Steps: []string{"check before", "copy database", "copy files", "update", "check after", "done before-update-20261127-160000.tar.gz"}},
			[]string{"Updated, and the website still works.", "<code>before-update-20261127-160000.tar.gz</code>"}, []string{"data-job="}},
		{agent.Job{ID: "jc0de000000000001", Kind: "wp-update", Site: "voltix-id", State: "failed", Steps: []string{"check before", "copy database", "copy files", "update", "check after", "restore", "restore database", "restore files", "switch files"},
			Error: "the update broke the website (WordPress does not load: PHP Fatal error), so it was put back as it was from the backup before-update-20261127-160000.tar.gz"},
			[]string{"The update broke the website, so the backup from just before it was put back by itself.", "Put back", "Broken by the update: putting the backup back", "PHP Fatal error"}, nil},
		{agent.Job{ID: "jc0de000000000001", Kind: "wp-update", Site: "voltix-id", State: "failed", Steps: []string{"check before", "copy database", "copy files", "update", "check after"}, Error: "Error: Could not create directory. — the website still works"},
			[]string{"The update did not finish.", "Could not create directory"}, []string{"Put back"}},
		{agent.Job{ID: "jc0de000000000001", Kind: "backup", Site: "voltix-id", State: "done"}, nil, []string{`id="job"`}},
		{agent.Job{ID: "jc0de000000000001", Kind: "wp-update", Site: "other-id", State: "done"}, nil, []string{`id="job"`}},
	}
	for i, c := range cases {
		f.wpJob = c.job
		for _, lang := range []string{"id", "en"} {
			e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
			code, body, _ := e.get(e.c, "/wp/voltix-id?tab=plugins&job=jc0de000000000001")
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("case %d (%s): %d, missing key %v", i, lang, code, missingKey.FindString(body))
			}
			if lang == "id" {
				continue
			}
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("case %d lacks %q", i, w)
				}
			}
			for _, w := range c.not {
				if strings.Contains(body, w) {
					t.Errorf("case %d shows %q", i, w)
				}
			}
		}
	}
	// the progress poll knows the steps
	f.wpJob = cases[0].job
	if _, body, _ := e.get(e.c, "/sites/job/jc0de000000000001/status"); !strings.Contains(body, `"check after"`) {
		t.Errorf("status lacks the steps still to come: %s", body)
	}
}

func TestWPStaging(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, setup := range []func(){func() { f.stgCopy, f.stgOf = false, "" }, func() { f.stgCopy = true }, func() { f.stgCopy, f.stgOf = false, "shop-id" }} {
			setup()
			if code, body, _ := e.get(e.c, "/wp/voltix-id"); code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s: %d, missing key %v", lang, code, missingKey.FindString(body))
			}
		}
	}
	f.stgCopy, f.stgOf = false, ""
	_, page, _ := e.get(e.c, "/wp/voltix-id")
	if !strings.Contains(page, `name="op" value="staging"`) || strings.Contains(page, `value="push"`) {
		t.Fatal("no staging copy: the page should offer to make one, and no push")
	}
	code, _, h := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"staging"}}, page)
	if code != 303 || h.Get("Location") != "/sites/job/jc0de000000000002" {
		t.Fatalf("make staging: %d %q", code, h.Get("Location"))
	}
	f.wpJob = agent.Job{ID: "jc0de000000000002", Kind: "staging", Site: "voltix-id", State: "running", Steps: []string{"files", "pull images", "start containers", "install WordPress", "copy files"}}
	if _, body, _ := e.get(e.c, "/sites/job/jc0de000000000002"); !strings.Contains(body, `data-step="addresses"`) || missingKey.MatchString(body) {
		t.Errorf("staging job page lacks the copy steps %v", missingKey.FindString(body))
	}
	f.wpJob.State = "done"
	if _, body, _ := e.get(e.c, "/sites/job/jc0de000000000002"); !strings.Contains(body, "The staging copy is ready") || strings.Contains(body, "pwgone") {
		t.Error("staging job done page")
	}
	// with a copy: open it, push it (confirmed), never from a name the form made up
	f.stgCopy = true
	_, page, _ = e.get(e.c, "/wp/voltix-id")
	if !strings.Contains(page, `href="/wp/staging-voltix-id"`) || !strings.Contains(page, `name="from" value="staging-voltix-id"`) || strings.Contains(page, `value="staging">`) {
		t.Fatal("copy not shown")
	}
	if code, _, _ := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"push"}, "from": {"staging-voltix-id"}}, page); code != 400 {
		t.Errorf("push without confirm answered %d", code)
	}
	if code, _, _ := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"push"}, "from": {"../x"}, "confirm": {"1"}}, page); code != 400 {
		t.Errorf("push from a bad name answered %d", code)
	}
	code, _, h = e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"push"}, "from": {"staging-voltix-id"}, "confirm": {"1"}}, page)
	if code != 303 || h.Get("Location") != "/wp/voltix-id?job=jc0de000000000003#job" || f.wpCalls[len(f.wpCalls)-1] != `/v1/sites/voltix-id/staging/push {"from":"staging-voltix-id"}` {
		t.Fatalf("push: %d %q %v", code, h.Get("Location"), f.wpCalls[len(f.wpCalls)-1])
	}
	// the push job on the live page uses its own words and steps
	f.wpJob = agent.Job{ID: "jc0de000000000003", Kind: "push", Site: "voltix-id", State: "running", Steps: []string{"safety database", "safety files", "copy files"}}
	_, body, _ := e.get(e.c, "/wp/voltix-id?job=jc0de000000000003")
	if !strings.Contains(body, "Push staging to live") || !strings.Contains(body, "Copying the staging files") || !strings.Contains(body, `data-step="addresses"`) {
		t.Error("push job card")
	}
	// on the staging copy itself: it says so and pushes to its live website without naming it in the form
	f.stgCopy, f.stgOf = false, "shop-id"
	_, page, _ = e.get(e.c, "/wp/voltix-id")
	if !strings.Contains(page, "This is a staging copy") || !strings.Contains(page, `href="/wp/shop-id"`) || strings.Contains(page, `name="from"`) {
		t.Fatal("staging page")
	}
	code, _, h = e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"push"}, "from": {"other-id"}, "confirm": {"1"}}, page)
	if code != 303 || h.Get("Location") != "/wp/shop-id?job=jc0de000000000003#job" || f.wpCalls[len(f.wpCalls)-1] != `/v1/sites/shop-id/staging/push {"from":"voltix-id"}` {
		t.Fatalf("push from the staging page: %d %q %v", code, h.Get("Location"), f.wpCalls[len(f.wpCalls)-1])
	}
	if code, _, _ := e.postWith(e.c, "/wp/voltix-id", url.Values{"op": {"staging"}}, page); code != 400 {
		t.Errorf("staging of a staging copy answered %d", code)
	}
}
