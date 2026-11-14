package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestImagesCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/sites/voltix-id")
		if code != 200 || !strings.Contains(page, `id="images"`) || strings.Contains(page, `value="image-update"`) || !strings.Contains(page, "mariadb:11.8 · c4124d1a") || missingKey.MatchString(page) {
			t.Fatalf("card up to date (%s) %d %v", lang, code, missingKey.FindString(page))
		}
	}
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	code, _, hdr := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"image-update"}}, page)
	if code != 303 || !strings.Contains(hdr.Get("Location"), "err=") || f.imgCalls != 1 {
		t.Errorf("update with nothing to do %d %s", code, hdr.Get("Location"))
	}
	f.imgOld = true
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, page, _ = e.get(e.c, "/sites/voltix-id")
		if !strings.Contains(page, `value="image-update"`) || !strings.Contains(page, "sha256:0ld0ld0ld0ld") || !strings.Contains(page, "data-confirm=") || missingKey.MatchString(page) {
			t.Fatalf("card with update (%s) %v", lang, missingKey.FindString(page))
		}
	}
	code, _, hdr = e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"image-update"}}, page)
	if code != 303 || hdr.Get("Location") != "/sites/job/jabcdef0123456789" {
		t.Fatalf("update %d %s", code, hdr.Get("Location"))
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, job, _ := e.get(e.c, "/sites/job/jabcdef0123456789")
		if code != 200 || !strings.Contains(job, `href="/sites/voltix-id#images"`) || !strings.Contains(job, `href="/backup/voltix-id"`) || missingKey.MatchString(job) {
			t.Fatalf("job page (%s) %d %v", lang, code, missingKey.FindString(job))
		}
	}
}

func TestShortImage(t *testing.T) {
	for in, want := range map[string]string{
		"":                                     "-",
		"mariadb:11.8@sha256:c4124d1afa875e47": "mariadb:11.8 · c4124d1a",
		"nostek-local/laravel:php8.4-0123456789ab": "nostek-local/laravel:php8.4-0123456789ab",
		"sha256:0123456789abcdef0123":              "sha256:0123456789ab",
	} {
		if got := shortImage(in); got != want {
			t.Errorf("shortImage(%q) = %q, want %q", in, got, want)
		}
	}
}
