package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestDiskPages(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/disk/voltix-id")
		if code != 200 || !strings.Contains(page, `id="disk-parts"`) || !strings.Contains(page, `href="/disk/voltix-id?path=%2fwp-content"`) ||
			!strings.Contains(page, "video.mp4") || !strings.Contains(page, `aria-current="page"`) || missingKey.MatchString(page) {
			t.Fatalf("site disk (%s) %d %v", lang, code, missingKey.FindString(page))
		}
		// UI-DISK-1: the folder icon of a row sits in its sized wrapper, never bare in the link
		if !strings.Contains(page, `<span class="row-ico" aria-hidden="true"><svg`) || strings.Contains(page, `"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2`) && !strings.Contains(page, `row-ico" aria-hidden="true"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7a2`) {
			t.Errorf("folder icon not in its sized wrapper (%s)", lang)
		}
		code, page, _ = e.get(e.c, "/disk")
		if code != 200 || !strings.Contains(page, `id="disk-docker"`) || !strings.Contains(page, `href="/disk/voltix-id"`) || !strings.Contains(page, `value="images"`) || missingKey.MatchString(page) {
			t.Fatalf("server disk (%s) %d %v", lang, code, missingKey.FindString(page))
		}
		if !strings.Contains(page, `value="build-cache"`) || !strings.Contains(page, "disabled") {
			t.Errorf("empty build cache not disabled (%s)", lang)
		}
		if _, dash, _ := e.get(e.c, "/"); !strings.Contains(dash, `href="/disk"`) {
			t.Errorf("dashboard has no disk link (%s)", lang)
		}
	}
	if _, page, _ := e.get(e.c, "/disk/voltix-id?path=/nope"); !strings.Contains(page, "no such file") {
		t.Error("agent error not shown")
	}
	if f.diskPaths[0] != "/" {
		t.Errorf("paths %v", f.diskPaths)
	}
	_, page, _ := e.get(e.c, "/disk")
	code, _, hdr := e.postWith(e.c, "/disk", url.Values{"what": {"images"}}, page)
	if code != 303 || !strings.Contains(hdr.Get("Location"), "done=images") || len(f.cleans) != 1 || f.cleans[0] != `{"what":"images"}` {
		t.Fatalf("clean %d %s %v", code, hdr.Get("Location"), f.cleans)
	}
	if code, _, _ := e.postWith(e.c, "/disk", url.Values{"what": {"volumes"}}, page); code != 400 || len(f.cleans) != 1 {
		t.Errorf("volumes clean-up %d", code)
	}
	_, done, _ := e.get(e.c, "/disk?done=images&freed=2+images")
	if !strings.Contains(done, "2 images") {
		t.Error("freed size not shown")
	}
	if code, _, _ := e.get(e.c, "/disk/Bad_ID"); code != 404 {
		t.Errorf("bad id %d", code)
	}
}
