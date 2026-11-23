package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestStaticReleasesPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.app = "static"
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		if code, body, _ := e.get(e.c, "/sites/voltix-id"); code != 200 || missingKey.MatchString(body) {
			t.Errorf("overview (%s): %d %v", lang, code, missingKey.FindString(body))
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	for _, want := range []string{`id="releases"`, `action="/sites/voltix-id/releases"`, "<code>dist &lt;b&gt;.zip</code>", `name="release" value="2"`, "The files before the first upload"} {
		if !strings.Contains(page, want) {
			t.Errorf("overview lacks %q", want)
		}
	}
	if strings.Contains(page, `name="release" value="3"`) || strings.Contains(page, `name="release" value="0"`) {
		t.Error("the live or a removed release has a rollback button")
	}
	tok := csrfRe.FindStringSubmatch(page)[1]
	// the token must come first; only .zip files
	if code, _ := e.multipartPost(e.c, "/sites/voltix-id/releases", [][2]string{{"@zip", "site.zip=PK"}, {"csrf", tok}}); code != 403 {
		t.Errorf("token after the file: %d", code)
	}
	if _, h := e.multipartPost(e.c, "/sites/voltix-id/releases", [][2]string{{"csrf", tok}, {"@zip", "site.tar=x"}}); !strings.Contains(h.Get("Location"), "err=") {
		t.Errorf("not a zip: %s", h.Get("Location"))
	}
	if _, h := e.multipartPost(e.c, "/sites/voltix-id/releases", [][2]string{{"csrf", tok}, {"@zip", `C:\build\site.zip=PKzip`}}); h.Get("Location") != "/sites/voltix-id?done=static-publish#releases" {
		t.Errorf("publish: %s", h.Get("Location"))
	}
	if _, _, h := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"static-rollback"}, "release": {"2"}}, "/sites/voltix-id"); h.Get("Location") != "/sites/voltix-id?done=static-rollback#releases" {
		t.Errorf("rollback: %s", h.Get("Location"))
	}
	if got := strings.Join(f.actions, "|"); !strings.Contains(got, "publish site.zip PKzip") || !strings.Contains(got, "static-rollback 2") || strings.Contains(got, "site.tar") {
		t.Errorf("agent calls %s", got)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id release 4 site.zip (1 files)") || !strings.Contains(audit, "voltix-id release 2") {
		t.Error("audit lacks the releases")
	}
	// not on other websites
	f.app = "wordpress"
	_, wp, _ := e.get(e.c, "/sites/voltix-id")
	if strings.Contains(wp, `id="releases"`) {
		t.Error("a WordPress website shows releases")
	}
	if code, _, h := e.post(e.c, "/sites/voltix-id", url.Values{"action": {"static-rollback"}, "release": {"2"}}, "/sites/voltix-id"); code != 400 {
		t.Errorf("rollback on WordPress: %d %s", code, h.Get("Location"))
	}
}
