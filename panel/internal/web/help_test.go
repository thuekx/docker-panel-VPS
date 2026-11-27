package web

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The panel's copy of the guides and the CHANGELOG (web/docs) is what the Help pages show; it must match the
// repository's. Run sh panel/sync-docs.sh after changing either.
func TestDocsCopy(t *testing.T) {
	want, _ := filepath.Glob("../../../docs/guides/*.md")
	got, _ := filepath.Glob("../../web/docs/guides/*.md")
	if len(want) == 0 || len(want) != len(got) {
		t.Fatalf("guides: %d in docs/guides, %d in panel/web/docs/guides: run sh panel/sync-docs.sh", len(want), len(got))
	}
	pairs := map[string]string{"../../../CHANGELOG.md": "../../web/docs/CHANGELOG.md"}
	for _, f := range want {
		pairs[f] = filepath.Join("../../web/docs/guides", filepath.Base(f))
	}
	for a, b := range pairs {
		x, err1 := os.ReadFile(a)
		y, err2 := os.ReadFile(b)
		if err1 != nil || err2 != nil || !bytes.Equal(x, y) {
			t.Errorf("%s differs from %s: run sh panel/sync-docs.sh", b, a)
		}
	}
}

func TestHelpPages(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	for _, p := range []string{"/help", "/help/changelog", "/help/update"} {
		if code, _, h := e.get(e.c, p); code != 303 || h.Get("Location") != "/login" {
			t.Fatalf("%s reachable without login: %d", p, code)
		}
	}
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, p := range []string{"/help", "/help/changelog", "/help/update", "/help/change-default-user", "/help/api"} {
			code, body, _ := e.get(e.c, p)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", p, lang, code, missingKey.FindString(body))
			}
			if !strings.Contains(body, `href="/help" class="on"`) {
				t.Errorf("%s (%s): Help not marked in the menu", p, lang)
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/help")
	for _, want := range []string{`href="/help/update"`, `href="/help/server-backup"`, `href="/help/change-default-user"`, "Change the default VPS user and SSH port safely", `href="/help/changelog#v1-`} {
		if !strings.Contains(body, want) {
			t.Errorf("index lacks %q", want)
		}
	}
	if strings.Contains(body, "(GUIDE-01)") {
		t.Error("index shows the step id in a title")
	}
	_, body, _ = e.get(e.c, "/help/changelog")
	for _, want := range []string{`id="v1-0-0-`, `id="v1-49-0-2026-11-27"`, `href="#v1-49-0-2026-11-27"`, "Setup lock for ready apps", "Changelog</h1>"} {
		if !strings.Contains(body, want) {
			t.Errorf("changelog lacks %q", want)
		}
	}
	// links in the CHANGELOG to guides open the panel's page, other repository files open on GitHub
	if !strings.Contains(body, `href="/help/install"`) || strings.Contains(body, `href="docs/guides/`) {
		t.Error("changelog links to guides are not the panel's pages")
	}
	_, body, _ = e.get(e.c, "/help/change-default-user")
	for _, want := range []string{"<table>", "<pre><code>", "sudo adduser NEWUSER", "<blockquote>", `href="#step-1-create-the-new-user-as-olduser"`} {
		if !strings.Contains(body, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
	for _, p := range []string{"/help/nope", "/help/..%2fCHANGELOG", "/help/UPDATE"} {
		if code, _, _ := e.get(e.c, p); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	// pages with a guide link to it at the top; the footer links the changelog
	_, body, _ = e.get(e.c, "/settings/update")
	if !strings.Contains(body, `class="guide-link" href="/help/update"`) || !strings.Contains(body, `<a href="/help/changelog">`) {
		t.Error("update page lacks its guide or changelog link")
	}
	if _, body, _ = e.get(e.c, "/sites"); strings.Contains(body, `class="guide-link"`) {
		t.Error("a page without a guide shows a guide link")
	}
}

func TestRunningReleaseMarked(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.srv.cfg.Version = "v1.48.0"
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/help/changelog")
	if !strings.Contains(body, `data-running="v1-48-0-2026-11-27"`) || !strings.Contains(body, `href="#v1-48-0-2026-11-27" aria-current="true"`) {
		t.Error("the running release is not marked")
	}
}
