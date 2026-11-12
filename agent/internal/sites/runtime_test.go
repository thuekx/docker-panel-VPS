package sites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestGoAndPythonSites(t *testing.T) {
	for _, c := range []struct{ app, ver, start, file string }{
		{"go", "1.27", `"exec ./server"`, "main.go"},
		{"python", "3.14", `"exec .venv/bin/python app.py"`, "app.py"},
	} {
		m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
		m.cfg.FileUID = os.Getuid()
		job, s, err := m.Create(context.Background(), spec.Create{App: c.app, Domain: "voltix.id", Primary: "domain", Version: c.ver, Source: "files"})
		if err != nil {
			t.Fatal(err)
		}
		if v := waitJob(t, m, job); v.State != "done" {
			t.Fatalf("%s job %+v", c.app, v)
		}
		cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
		rt, _ := catalog.RuntimeOf(c.app, c.ver)
		for _, want := range []string{"image: " + rt.Image, "CMD-SHELL", "curl -s -o /dev/null -w '%{http_code}'", "$${c:-0}", c.start, `PORT: "3000"`} {
			if !strings.Contains(string(cf), want) {
				t.Errorf("%s compose lacks %q", c.app, want)
			}
		}
		if strings.Contains(string(cf), "NODE_ENV") {
			t.Errorf("%s compose holds NODE_ENV", c.app)
		}
		if _, err := os.Stat(filepath.Join(m.wpDir(s.ID), "source", c.file)); err != nil {
			t.Errorf("%s starter: %v", c.app, err)
		}
		var build string
		for _, call := range f.calls {
			if len(call) > 0 && call[0] == "run" {
				build = strings.Join(call, " ")
			}
		}
		if !strings.Contains(build, spec.InstallCmd[c.app]) || !strings.Contains(build, spec.DefaultBuild[c.app]) || !strings.Contains(build, rt.Image) {
			t.Errorf("%s build %s", c.app, build)
		}
	}
	if _, ok := catalog.RuntimeOf("go", "1.20"); ok {
		t.Error("old Go offered")
	}
	if (spec.Create{App: "python", Domain: "voltix.id", Primary: "domain", Version: "2.7", Source: "files"}).Check(nil) == nil {
		t.Error("Python 2.7 accepted")
	}
}
