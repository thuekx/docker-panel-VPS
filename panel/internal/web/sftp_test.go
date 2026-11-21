package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func TestSFTPPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	f.sftp = agent.SFTP{Port: 2222}
	_, body, _ := e.get(e.c, "/sftp/voltix-id")
	for _, want := range []string{"No SFTP account yet.", "comes with the panel update", `type="submit" data-busy="Applying…" disabled>Create account`, `href="/sftp/voltix-id" aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty page lacks %q", want)
		}
	}
	f.sftp = agent.SFTP{Ready: true, Port: 2222, Accounts: []agent.SFTPAccount{
		{Name: "ana", Dir: "/", Password: true, Keys: []agent.SFTPKey{{Type: "ssh-ed25519", Fingerprint: "SHA256:abc", Comment: "<b>laptop"}}},
		{Name: "budi.up", Dir: "/wp-content/uploads", Password: true}}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/sftp/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ = e.get(e.c, "/sftp/voltix-id")
	for _, want := range []string{"sftp://ana@", ":2222", "&lt;b&gt;laptop", "SHA256:abc",
		`name="op" value="key-remove"><input type="hidden" name="name" value="ana"><input type="hidden" name="fingerprint" value="SHA256:abc">`,
		`name="op" value="password-remove"><input type="hidden" name="name" value="ana">`, "1 key(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="op" value="password-remove"><input type="hidden" name="name" value="budi.up">`) {
		t.Error("an account without keys offers to remove its password")
	}
	pass := "a-long-password-1"
	for _, c := range []struct {
		form url.Values
		loc  string
		sent string
	}{
		{url.Values{"op": {"user-add"}, "name": {"cici"}, "dir": {"/wp-content"}, "password": {pass}, "password2": {pass}}, "done=user-add", `"op":"user-add","name":"cici","hash":"$2a$`},
		{url.Values{"op": {"user-add"}, "name": {"dodi"}, "key": {" ssh-ed25519 AAAA dodi "}}, "done=user-add", `{"op":"user-add","name":"dodi","key":"ssh-ed25519 AAAA dodi"}`},
		{url.Values{"op": {"password-set"}, "name": {"ana"}, "password": {pass}, "password2": {pass}}, "done=password-set", `"op":"password-set","name":"ana","hash":"$2a$`},
		{url.Values{"op": {"password-remove"}, "name": {"ana"}}, "done=password-remove", `{"op":"password-remove","name":"ana"}`},
		{url.Values{"op": {"key-add"}, "name": {"ana"}, "key": {"ssh-ed25519 BBBB"}}, "done=key-add", `{"op":"key-add","name":"ana","key":"ssh-ed25519 BBBB"}`},
		{url.Values{"op": {"key-remove"}, "name": {"ana"}, "fingerprint": {"SHA256:abc"}}, "done=key-remove", `{"op":"key-remove","name":"ana","fingerprint":"SHA256:abc"}`},
		{url.Values{"op": {"dir-set"}, "name": {"ana"}, "dir": {"/"}}, "done=same", `{"op":"dir-set","name":"ana","dir":"/"}`},
		{url.Values{"op": {"user-remove"}, "name": {"budi.up"}, "confirm": {"1"}}, "done=user-remove", `{"op":"user-remove","name":"budi.up"}`},
		{url.Values{"op": {"user-add"}, "name": {"taken"}, "password": {pass}, "password2": {pass}}, "err=", `"name":"taken"`},
	} {
		code, _, h := e.post(e.c, "/sftp/voltix-id", c.form, "/sftp/voltix-id")
		if code != 303 || !strings.Contains(h.Get("Location"), c.loc) {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
		if n := len(f.sftpCalls); n == 0 || !strings.Contains(f.sftpCalls[n-1], c.sent) || strings.Contains(f.sftpCalls[n-1], pass) {
			t.Errorf("%v sent %v", c.form, f.sftpCalls)
		}
	}
	n := len(f.sftpCalls)
	for _, bad := range []url.Values{
		{"op": {"user-add"}, "name": {"eka"}},
		{"op": {"user-add"}, "name": {"eka"}, "password": {"short"}, "password2": {"short"}},
		{"op": {"user-add"}, "name": {"eka"}, "password": {pass}, "password2": {pass + "x"}},
		{"op": {"password-set"}, "name": {"ana"}},
		{"op": {"key-add"}, "name": {"ana"}},
		{"op": {"user-remove"}, "name": {"ana"}},
		{"op": {"user-add"}, "name": {""}, "password": {pass}, "password2": {pass}},
	} {
		if _, _, h := e.post(e.c, "/sftp/voltix-id", bad, "/sftp/voltix-id"); !strings.Contains(h.Get("Location"), "err=") {
			t.Errorf("%v: %s", bad, h.Get("Location"))
		}
	}
	if len(f.sftpCalls) != n {
		t.Errorf("bad input reached the agent: %v", f.sftpCalls[n:])
	}
	if code, _, _ := e.post(e.c, "/sftp/voltix-id", url.Values{"op": {"shell"}, "name": {"ana"}}, "/sftp/voltix-id"); code != 400 {
		t.Errorf("unknown op: %d", code)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id user-add cici dir /wp-content") || !strings.Contains(audit, "key-remove ana SHA256:abc") || strings.Contains(audit, "$2a$") {
		t.Error("audit lines")
	}
}
