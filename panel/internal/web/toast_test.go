package web

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// The result of an action also shows as a toast at the bottom right: every signed-in page has the toast box and its
// close label, and the result banners are marked for it.
func TestToasts(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for lang, close := range map[string]string{"en": "Close", "id": "Tutup"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/sites?done=removed")
		if code != 200 || missingKey.MatchString(page) {
			t.Fatalf("%s: %d %v", lang, code, missingKey.FindString(page))
		}
		for _, want := range []string{`<div class="toasts" id="toasts">`, `data-toastclose="` + close + `"`, `<p data-toast class="banner ok"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: no %s", lang, want)
			}
		}
		if _, page, _ := e.get(e.c, "/sites"); strings.Contains(page, "data-toast class") {
			t.Errorf("%s: a toast without a result", lang)
		}
	}
	js, err := os.ReadFile("../../web/static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"function toasts()", `all("[data-toast]")`, "toasts(); });", "5000"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js: no %s", want)
		}
	}
}
