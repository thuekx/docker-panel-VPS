package sites

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestStaticReleases(t *testing.T) {
	m, _, s, _ := appSite(t, spec.Create{App: "static", Domain: "voltix.id", Primary: "domain", Server: "nginx", Title: "Voltix"})
	wp := m.wpDir(s.ID)
	_ = os.MkdirAll(filepath.Join(wp, errDir), 0o755)
	_ = os.WriteFile(filepath.Join(wp, errDir, "404.html"), []byte("mine"), 0o644)
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(wp, p)); return string(b) }
	starter := read("index.html")

	// refused: no index.html, a symbolic link, not a zip
	for _, bad := range [][]byte{mkzip(t, map[string]string{"a/b.html": "x"}, ""), mkzip(t, map[string]string{"index.html": "x"}, "evil"), []byte("nope")} {
		if _, err := m.PublishStatic(s.ID, "bad.zip", bytes.NewReader(bad)); !errors.Is(err, ErrBadInput) {
			t.Errorf("bad zip: %v", err)
		}
	}
	if read("index.html") != starter {
		t.Fatal("a refused zip changed the live site")
	}
	// one top folder is stripped; the website's error pages stay
	r1, err := m.PublishStatic(s.ID, "../dist\x01.zip", bytes.NewReader(mkzip(t, map[string]string{"dist/index.html": "one", "dist/assets/app.js": "js", "dist/" + errDir + "/404.html": "theirs"}, "")))
	if err != nil || r1.Release != 1 || r1.Name != "dist.zip" || r1.Files != 3 {
		t.Fatalf("publish 1: %+v %v", r1, err)
	}
	if read("index.html") != "one" || read("assets/app.js") != "js" || read(errDir+"/404.html") != "mine" {
		t.Fatalf("live after 1: %q %q %q", read("index.html"), read("assets/app.js"), read(errDir+"/404.html"))
	}
	for i, c := range []string{"two", "three", "four"} {
		if r, err := m.PublishStatic(s.ID, c+".zip", bytes.NewReader(mkzip(t, map[string]string{"index.html": c}, ""))); err != nil || r.Release != i+2 {
			t.Fatalf("publish %s: %+v %v", c, r, err)
		}
	}
	if read("index.html") != "four" || read("assets/app.js") != "" {
		t.Fatalf("live after 4: %q", read("index.html"))
	}
	h, _ := m.Releases(s.ID)
	var got []string
	for _, x := range h {
		got = append(got, x.Result)
	}
	// 4 live, 3 and 2 kept, 1 and the starter (0) removed
	if strings.Join(got, ",") != "live,ok,ok,removed,removed" {
		t.Fatalf("history %v", got)
	}
	for n, want := range map[int]bool{0: false, 1: false, 2: true, 3: true} {
		if _, err := os.Stat(m.staticRel(s.ID, n)); (err == nil) != want {
			t.Errorf("release %d on disk: %v", n, err == nil)
		}
	}
	if _, err := m.RollbackStatic(s.ID, 1); !errors.Is(err, ErrBadInput) {
		t.Errorf("rollback to a removed release: %v", err)
	}
	if r, err := m.RollbackStatic(s.ID, 2); err != nil || !r.Rollback || r.Result != "live" {
		t.Fatalf("rollback: %+v %v", r, err)
	}
	if read("index.html") != "two" || read(errDir+"/404.html") != "mine" {
		t.Fatalf("live after rollback: %q", read("index.html"))
	}
	if _, err := os.Stat(m.staticRel(s.ID, 4)); err != nil {
		t.Error("the release rolled back from is not kept")
	}
	if _, err := os.Stat(m.staticRel(s.ID, 2)); err == nil {
		t.Error("the live release still has a folder")
	}
	if fi, err := os.Stat(filepath.Join(m.staticDir(s.ID), staticHistory)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("history file: %v", err)
	}
	// only static websites
	mw, _, w := readySite(t)
	if _, err := mw.PublishStatic(w, "x.zip", bytes.NewReader(nil)); !errors.Is(err, ErrNotStatic) {
		t.Errorf("wordpress: %v", err)
	}
}
