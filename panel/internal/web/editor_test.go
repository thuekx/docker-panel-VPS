package web

import (
	"regexp"
	"strings"
	"testing"
)

// EDITOR-1: the edit page loads the code editor with a fresh style nonce that only its own CSP allows.
func TestEditorPage(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	nonceRe := regexp.MustCompile(`data-nonce="([A-Za-z0-9_-]{24})"`)
	var seen string
	for i := 0; i < 2; i++ {
		code, body, h := e.get(e.c, "/files/voltix-id/edit?path=/index.php")
		m := nonceRe.FindStringSubmatch(body)
		if code != 200 || m == nil || !strings.Contains(body, `<script src="/static/vendor/codemirror.js" defer></script>`) || !strings.Contains(body, `data-name="/index.php"`) || !strings.Contains(body, "data-editor-bar hidden") {
			t.Fatalf("edit page %d: nonce %v", code, m)
		}
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self' 'nonce-"+m[1]+"';") || !strings.Contains(csp, "script-src 'self';") {
			t.Errorf("csp %q", csp)
		}
		if m[1] == seen {
			t.Error("the nonce is reused")
		}
		seen = m[1]
	}
	_, body, h := e.get(e.c, "/files/voltix-id/edit?path=/logo.png")
	if strings.Contains(body, "codemirror.js") || strings.Contains(h.Get("Content-Security-Policy"), "nonce-") {
		t.Error("a binary file loads the editor")
	}
	if _, _, h := e.get(e.c, "/files/voltix-id"); strings.Contains(h.Get("Content-Security-Policy"), "nonce-") || !strings.Contains(h.Get("Content-Security-Policy"), "style-src 'self';") {
		t.Errorf("other pages: %q", h.Get("Content-Security-Policy"))
	}
}
