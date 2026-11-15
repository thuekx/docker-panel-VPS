package web

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

var missingKey = regexp.MustCompile(`\[[a-z0-9]+\.[a-z0-9. ]+\]`)

func TestProfileNeedsLoginAndShowsNoMissingKeys(t *testing.T) {
	e := newEnv(t)
	if code, _, h := e.get(e.c, "/profile"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("profile reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/profile", "/settings", "/"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Fatalf("%s (%s): %d or a missing translation key", p, lang, code)
			}
		}
	}
}

func TestProfileAppearanceSavesThemeAndAccent(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/")
	if strings.Contains(body, "data-accent=") {
		t.Fatal("teal is the default and needs no attribute")
	}
	_, prof, _ := e.get(e.c, "/profile")
	if code, _, _ := e.postWith(e.c, "/profile/appearance", url.Values{"theme": {"dark"}, "accent": {"neon"}}, prof); code != 400 {
		t.Fatalf("unknown accent accepted: %d", code)
	}
	if code, _, _ := e.postWith(e.c, "/profile/appearance", url.Values{"theme": {"sepia"}, "accent": {"blue"}}, prof); code != 400 {
		t.Fatalf("unknown theme accepted: %d", code)
	}
	code, _, h := e.postWith(e.c, "/profile/appearance", url.Values{"theme": {"light"}, "accent": {"violet"}}, prof)
	if code != 303 || h.Get("Location") != "/profile?saved=look" {
		t.Fatalf("appearance: %d %s", code, h.Get("Location"))
	}
	_, body, _ = e.get(e.c, "/sites")
	if !strings.Contains(body, `data-theme="light"`) || !strings.Contains(body, `data-accent="violet"`) {
		t.Fatal("theme or accent not applied")
	}
	// without the form token nothing changes
	if code, _, _ := e.postWith(e.c, "/profile/appearance", url.Values{"theme": {"dark"}, "accent": {"rose"}}, ""); code != 403 {
		t.Fatalf("appearance without CSRF: %d", code)
	}
}

func TestProfilePasswordChange(t *testing.T) {
	e := newEnv(t)
	secret, _ := e.setupAdmin(e.c, "admin@example.test")
	// a second signed-in browser that must be signed out by the change
	other := e.client()
	e.clock = e.clock.Add(time.Minute)
	e.post(other, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login")
	totp, _ := auth.TOTPCode(secret, e.clock)
	e.post(other, "/login/2fa", url.Values{"code": {totp}}, "/login/2fa")
	if code, _, _ := e.get(other, "/"); code != 200 {
		t.Fatal("second browser not signed in")
	}
	const newPw = "fake-New-pass-456789"
	try := func(cur, p1, p2 string) (int, string) {
		_, prof, _ := e.get(e.c, "/profile")
		code, body, _ := e.postWith(e.c, "/profile/password", url.Values{"current": {cur}, "password": {p1}, "password2": {p2}}, prof)
		return code, body
	}
	if code, body := try("wrong-password-000", newPw, newPw); code != 422 || !strings.Contains(body, "current password is wrong") {
		t.Fatalf("wrong current password: %d", code)
	}
	if code, _ := try(pw, newPw, newPw+"x"); code != 422 {
		t.Fatalf("mismatch accepted: %d", code)
	}
	if code, _ := try(pw, "short", "short"); code != 422 {
		t.Fatalf("short password accepted: %d", code)
	}
	if code, _ := try(pw, pw, pw); code != 422 {
		t.Fatalf("same password accepted: %d", code)
	}
	code, _ := try(pw, newPw, newPw)
	if code != 303 {
		t.Fatalf("change: %d", code)
	}
	if code, _, _ := e.get(e.c, "/profile"); code != 200 {
		t.Fatal("this browser must stay signed in")
	}
	if code, _, h := e.get(other, "/"); code != 303 || h.Get("Location") != "/login" {
		t.Fatal("other browser still signed in after password change")
	}
	fresh := e.client()
	if code, _, h := e.post(fresh, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login"); code != 401 || h.Get("Location") != "" {
		t.Fatalf("old password still works: %d", code)
	}
	if code, _, h := e.post(fresh, "/login", url.Values{"email": {"admin@example.test"}, "password": {newPw}}, "/login"); code != 303 || h.Get("Location") != "/login/2fa" {
		t.Fatalf("new password refused: %d", code)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "account.password_changed") || strings.Contains(audit, newPw) {
		t.Fatal("audit must record the change and never the password")
	}
}

func TestJobProgress(t *testing.T) {
	cases := []struct {
		j    agent.Job
		want string
		pct  int
	}{
		{agent.Job{State: "running"}, "todo todo todo todo", 0},
		{agent.Job{State: "running", Steps: []string{"files"}}, "active todo todo todo", 0},
		{agent.Job{State: "running", Steps: []string{"files", "pull images", "start containers"}}, "done done active todo", 50},
		{agent.Job{State: "failed", Steps: []string{"files", "pull images"}}, "done failed todo todo", 25},
		{agent.Job{State: "done", Steps: jobSteps}, "done done done done", 100},
		{agent.Job{State: "running", Steps: []string{"files", "something new"}}, "done active todo todo todo", 20},
		// IMG-01: an image built on the server shows up before the recreate, where it runs
		{agent.Job{Kind: "image", State: "running", Steps: []string{"pull new images", "copy database", "copy files", "build image"}}, "done done done active todo", 60},
		{agent.Job{Kind: "image", State: "failed", Steps: []string{"pull new images", "copy database", "copy files", "recreate containers", "put back the previous images"}}, "done done done done failed", 80},
	}
	for _, c := range cases {
		rows, pct := jobProgress(c.j, agent.Site{})
		var got []string
		for _, r := range rows {
			got = append(got, r.State)
		}
		if strings.Join(got, " ") != c.want || pct != c.pct {
			t.Errorf("%v: got %v %d%%, want %s %d%%", c.j.Steps, got, pct, c.want, c.pct)
		}
	}
}

func TestCreateStepsFollowTheApp(t *testing.T) {
	for _, c := range []struct {
		site agent.Site
		want string
	}{
		{agent.Site{ID: "x"}, "files,pull images,start containers,install WordPress"},
		{agent.Site{ID: "x", App: "go"}, "files,pull images,fetch and build,start new release"},
		{agent.Site{ID: "x", App: "laravel"}, "files,build PHP image,pull images,install Laravel,start containers"},
		{agent.Site{ID: "x", App: "php", Database: true, DBEngine: "postgres"}, "files,build PHP image,pull images,start containers"},
		{agent.Site{ID: "x", App: "static"}, "files,pull images,start containers"},
		{agent.Site{ID: "x", App: "n8n"}, "files,pull images,start containers"},
	} {
		// right from the first poll, before the agent reports the app's own steps
		rows, _ := jobProgress(agent.Job{Kind: "create", State: "running", Steps: []string{"files", "pull images"}}, c.site)
		var keys []string
		for _, r := range rows {
			keys = append(keys, r.Key)
		}
		if strings.Join(keys, ",") != c.want {
			t.Errorf("%s: %v", c.site.AppName(), keys)
		}
	}
}

func TestJobStatusJSON(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	if code, _, _ := e.get(e.c, "/sites/job/j0123456789abcdef/status"); code != 303 {
		t.Fatal("job status reachable without login")
	}
	e.setupAdmin(e.c, "admin@example.test")
	code, body, h := e.get(e.c, "/sites/job/j0123456789abcdef/status")
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
		t.Fatalf("status: %d %s", code, h.Get("Content-Type"))
	}
	var v struct {
		State   string
		Percent int
		Steps   []stepRow
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || v.State != "done" || len(v.Steps) != 2 { // a finished job lists only the steps that ran
		t.Fatalf("status body: %v %s", err, body)
	}
	if strings.Contains(body, "Pw-For-Test-Only") || strings.Contains(body, "password") {
		t.Fatal("status must never carry the password")
	}
	if code, _, _ := e.get(e.c, "/sites/job/bad/status"); code != 404 {
		t.Fatalf("bad job id: %d", code)
	}
	if code, _, _ := e.get(e.c, "/sites/job/jffffffffffffffff/status"); code != 404 {
		t.Fatalf("unknown job: %d", code)
	}
}
