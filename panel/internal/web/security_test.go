package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// security answers the security routes of the fake agent.
func (f *fakeAgent) security(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
		if f.secJob == nil {
			write(404, map[string]string{"error": "job not found"})
			return
		}
		write(200, f.secJob)
		return
	}
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.secCalls = append(f.secCalls, strings.TrimSpace(string(b)))
		if strings.Contains(string(b), `"ip":"bad"`) {
			write(400, map[string]string{"error": "bad input: \"bad\" is not an IP address or network"})
			return
		}
		if strings.Contains(string(b), `"op":"hotlink-off"`) {
			write(200, map[string]string{"job": ""})
			return
		}
		write(202, map[string]string{"job": "jsec0000000000001"})
		return
	}
	write(200, agent.Security{Blocked: []string{"198.51.100.9", "2001:db8::/32"}, Protected: []agent.Protected{{Path: "/admin", Users: []string{"ana", "budi"}}, {Path: "/solo", Users: []string{"cici"}}},
		Hotlink: &agent.Hotlink{Allow: []string{"google.com"}}, HotlinkExts: []string{"jpg", "png"}, Job: f.secJob,
		WebServer: true, ErrorCodes: []int{403, 404, 503}, ErrorPages: []int{404}, ErrorDir: "/.nostek-errors", MIME: []agent.MIME{{Type: "application/wasm", Exts: []string{"wasm"}}}})
}

func TestSecurityPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/security/voltix-id"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	jobs := []*agent.Job{nil,
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "running", Steps: []string{"routes"}},
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "failed", Steps: []string{"routes"}, Error: "compose up failed"},
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "done", Steps: []string{"routes"}},
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, j := range jobs {
			f.secJob = j
			for _, q := range []string{"", "?job=jsec0000000000001", "?done=nochange"} {
				code, body, _ := e.get(e.c, "/security/voltix-id"+q)
				if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
					t.Errorf("%s (%s, %v): %d or missing key %v", q, lang, j, code, missingKey.FindString(body))
				}
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	f.secJob = nil
	_, body, _ := e.get(e.c, "/security/voltix-id")
	for _, want := range []string{
		`href="/security/voltix-id" aria-current="page"`,
		`name="ip" value="198.51.100.9"`,
		"<code>2001:db8::/32</code>",
		"Files: jpg, png",
		">google.com</textarea>",
		`name="user" value="ana"`,
		"Save allowed sites",
		`href="/files/voltix-id/edit?path=%2f.nostek-errors%2f404.html"`,
		`name="op" value="errpage-on"><input type="hidden" name="code" value="503">`,
		`name="op" value="errpage-off"><input type="hidden" name="code" value="404">`,
		"<code>application/wasm</code> <span class=\"muted\">.wasm</span>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="user" value="cici"><code>cici</code> <button`) {
		t.Error("the last user of a folder can be removed")
	}
	f.secJob = jobs[3]
	if _, body, _ = e.get(e.c, "/security/voltix-id?job=jsec0000000000001"); !strings.Contains(body, "Done. The rule is live.") {
		t.Error("finished job not shown")
	}
	f.secJob = nil
	pw := "correct horse"
	for _, c := range []struct {
		form url.Values
		loc  string
	}{
		{url.Values{"op": {"block-add"}, "ip": {" 203.0.113.7 "}}, "/security/voltix-id?job=jsec0000000000001#job"},
		{url.Values{"op": {"block-add"}, "ip": {""}}, "err="},
		{url.Values{"op": {"block-add"}, "ip": {"bad"}}, "err=bad+input"},
		{url.Values{"op": {"block-remove"}, "ip": {"198.51.100.9"}}, "#job"},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {"short"}, "password2": {"short"}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {pw}, "password2": {pw + "x"}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {""}, "user": {"ana"}, "password": {pw}, "password2": {pw}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {pw}, "password2": {pw}}, "#job"},
		{url.Values{"op": {"protect-user-remove"}, "path": {"/admin"}, "user": {"budi"}}, "#job"},
		{url.Values{"op": {"protect-remove"}, "path": {"/admin"}}, "err="},
		{url.Values{"op": {"protect-remove"}, "path": {"/admin"}, "confirm": {"1"}}, "#job"},
		{url.Values{"op": {"hotlink-on"}, "allow": {"google.com\r\nbing.com, duck.com"}}, "#job"},
		{url.Values{"op": {"hotlink-off"}}, "done=nochange"},
		{url.Values{"op": {"errpage-on"}, "code": {"503"}}, "#job"},
		{url.Values{"op": {"mime-add"}, "type": {"text/x-tex"}, "exts": {""}}, "err="},
		{url.Values{"op": {"mime-add"}, "type": {" text/x-tex "}, "exts": {"tex ltx"}}, "#job"},
		{url.Values{"op": {"mime-remove"}, "type": {"application/wasm"}}, "#job"},
	} {
		if code, _, h := e.post(e.c, "/security/voltix-id", c.form, "/security/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), c.loc) {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
	}
	if code, _, _ := e.post(e.c, "/security/voltix-id", url.Values{"op": {"nosuch"}}, "/security/voltix-id"); code != 400 {
		t.Errorf("odd op: %d", code)
	}
	var hashed agent.SecurityChange
	var rest []string
	for _, c := range f.secCalls {
		if strings.Contains(c, `"op":"protect-set"`) {
			_ = json.Unmarshal([]byte(c), &hashed)
			continue
		}
		rest = append(rest, c)
	}
	want := []string{`{"op":"block-add","ip":"203.0.113.7"}`, `{"op":"block-add","ip":"bad"}`, `{"op":"block-remove","ip":"198.51.100.9"}`,
		`{"op":"protect-user-remove","path":"/admin","user":"budi"}`, `{"op":"protect-remove","path":"/admin"}`,
		`{"op":"hotlink-on","allow":["google.com","bing.com","duck.com"]}`, `{"op":"hotlink-off"}`,
		`{"op":"errpage-on","code":503}`, `{"op":"mime-add","type":"text/x-tex","exts":"tex ltx"}`, `{"op":"mime-remove","type":"application/wasm"}`}
	if strings.Join(rest, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(rest, "\n"))
	}
	// only a bcrypt hash of the password reaches the agent
	if hashed.User != "ana" || bcrypt.CompareHashAndPassword([]byte(hashed.Hash), []byte(pw)) != nil || strings.Contains(strings.Join(f.secCalls, ""), pw) {
		t.Errorf("protect-set %+v", hashed)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id block-add 203.0.113.7") || !strings.Contains(audit, "protect-set /admin user ana") || strings.Contains(audit, "$2a$") {
		t.Error("audit lacks the security changes or holds a hash")
	}
}
