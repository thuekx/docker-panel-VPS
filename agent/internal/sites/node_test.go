package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const commitA = "0123456789abcdef0123456789abcdef01234567"

// nodeSite creates a Node.js website with a fake runner. build decides what the build container answers;
// up decides whether `compose up ... app` succeeds.
func nodeSite(t *testing.T, build func(n int) ([]byte, error), up func() error) (*Manager, *fakeRunner, Site, JobView) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	builds := 0
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "run" && strings.Contains(j, "np-build-"):
			builds++
			out, err := build(builds)
			return out, err, true
		case strings.Contains(j, " up -d --wait") && strings.HasSuffix(j, " app"):
			if up != nil {
				if err := up(); err != nil {
					return []byte("container unhealthy"), err, true
				}
			}
			return nil, nil, true
		}
		return nil, nil, false
	}
	job, s, err := m.Create(context.Background(), spec.Create{App: "node", Domain: "voltix.id", Primary: "domain",
		Version: "24", Repo: "https://github.com/owner/shop.git", Branch: "main", Start: "node server.js"})
	if err != nil {
		t.Fatal(err)
	}
	return m, f, s, waitJob(t, m, job)
}

func okBuild(n int) ([]byte, error) {
	return []byte("== git clone main\n" + commitMarker + commitA + " Release " + string(rune('0'+n)) + "\n== install\n== build done\n"), nil
}

func TestNodeSiteCreate(t *testing.T) {
	m, f, s, v := nodeSite(t, okBuild, nil)
	if v.State != "done" || strings.Join(v.Steps, ",") != "files,pull images,fetch and build,start new release" {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if !got.IsRuntime() || got.HasDB() || got.Release != 1 || got.Phase != "ready" || got.Repo != "https://github.com/owner/shop.git" {
		t.Fatalf("site %+v", got)
	}
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	n, _ := catalog.Node("24")
	for _, want := range []string{"image: " + n.Image, `user: "33:33"`, `command: ["sh", "-c", "exec node server.js"]`,
		`PORT: "3000"`, "./wp/releases/1:/app", `loadbalancer.server.port: "3000"`, "networks: [nostek-edge, out]",
		"path: ./deploy/app.env"} {
		if !strings.Contains(string(cf), want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	for _, bad := range []string{"db:", "php", "nginx", "secrets:"} {
		if strings.Contains(string(cf), bad) {
			t.Errorf("compose holds %q", bad)
		}
	}
	// the build container: limits, no privileges, repository and branch as environment values (never in the script)
	var build []string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "run" {
			build = c
		}
	}
	j := strings.Join(build, " ")
	for _, want := range []string{"--rm", "--memory 1536m", "--cap-drop ALL", "no-new-privileges:true", "--pids-limit 1024",
		"NOSTEK_REPO=https://github.com/owner/shop.git", "NOSTEK_BRANCH=main", "--user " + strconv.Itoa(m.uid()), n.Image,
		"/wp/releases/1:/app", spec.NodeInstall, spec.NodeBuild, `rm -rf "$HOME/src/.git"`, "NOSTEK_DIR=", `cp -R "$src/$NOSTEK_DIR/." /app/`} {
		if !strings.Contains(j, want) {
			t.Errorf("build lacks %q: %s", want, j)
		}
	}
	if !strings.HasPrefix(build[len(build)-1], "set -e\n") {
		t.Error("the build script does not stop at the first failing step")
	}
	if strings.Contains(build[len(build)-1], "github.com") {
		t.Error("the repository address is in the build script")
	}
	h, _ := m.History(s.ID)
	if len(h) != 1 || h[0].Result != "live" || h[0].Commit != commitA || h[0].Message != "Release 1" {
		t.Fatalf("history %+v", h)
	}
	if l, err := m.BuildLog(s.ID, 1); err != nil || !strings.Contains(l, "== build done") {
		t.Fatalf("log %q %v", l, err)
	}
	if st, _ := os.Stat(filepath.Join(m.deployDir(s.ID), "history.json")); st.Mode().Perm() != 0o600 {
		t.Errorf("history mode %v", st.Mode())
	}
	if svc := services(got); strings.Join(svc, ",") != "app" {
		t.Errorf("services %v", svc)
	}
}

func TestNodeFromFiles(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	job, s, err := m.Create(context.Background(), spec.Create{App: "node", Domain: "voltix.id", Primary: "domain",
		Version: "22", Source: "files", Dir: "apps/web", Title: "Toko Voltix"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if !got.FromFiles() || got.Dir != "apps/web" || got.Repo != "" {
		t.Fatalf("site %+v", got)
	}
	src := filepath.Join(m.wpDir(s.ID), "source")
	js, err := os.ReadFile(filepath.Join(src, "server.js"))
	if err != nil || !strings.Contains(string(js), "process.env.PORT") || strings.Count(string(js), "`") != 2 {
		t.Fatalf("starter %v\n%s", err, js)
	}
	if _, err := os.Stat(filepath.Join(src, "package.json")); err != nil {
		t.Fatal(err)
	}
	var build string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "run" {
			build = strings.Join(c, " ")
		}
	}
	if !strings.Contains(build, src+":/src:ro") || !strings.Contains(build, "NOSTEK_DIR=apps/web") || strings.Contains(build, "git clone") || strings.Contains(build, "NOSTEK_REPO") {
		t.Fatalf("build %s", build)
	}
}

func TestNodeFirstBuildFails(t *testing.T) {
	m, _, s, v := nodeSite(t, func(int) ([]byte, error) { return []byte("npm ERR! missing script"), errors.New("exit 1") }, nil)
	if v.State != "failed" || !strings.Contains(v.Error, "build failed") {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if got.Phase != "failed" {
		t.Fatalf("site %+v", got)
	}
	h, _ := m.readHistory(s.ID)
	if len(h) != 1 || h[0].Result != "failed" {
		t.Fatalf("history %+v", h)
	}
	if l, _ := m.BuildLog(s.ID, 1); !strings.Contains(l, "npm ERR!") {
		t.Fatalf("log %q", l)
	}
}

func deployAndWait(t *testing.T, m *Manager, id string) JobView {
	t.Helper()
	j, err := m.StartDeploy(id)
	if err != nil {
		t.Fatal(err)
	}
	return waitJob(t, m, j)
}

func TestNodeRedeployRollbackPrune(t *testing.T) {
	failNext := false
	m, _, s, _ := nodeSite(t, okBuild, func() error {
		if failNext {
			return errors.New("unhealthy")
		}
		return nil
	})
	// a release that builds but never answers: the previous one goes back, the website keeps serving
	failNext = true
	v := deployAndWait(t, m, s.ID)
	if v.State != "failed" || !strings.Contains(v.Error, "did not answer on port 3000") ||
		strings.Join(v.Steps, ",") != "fetch and build,start new release,previous release back" {
		t.Fatalf("job %+v", v)
	}
	failNext = false
	// putting release 1 back failed in the fake too (same switch), so look at what was asked: release 1 again
	got, _ := m.Get(s.ID)
	if got.Release != 1 {
		t.Fatalf("live release %d", got.Release)
	}
	for i := 0; i < 4; i++ {
		if v := deployAndWait(t, m, s.ID); v.State != "done" {
			t.Fatalf("deploy %d: %+v", i, v)
		}
	}
	got, _ = m.Get(s.ID)
	if got.Release != 6 {
		t.Fatalf("live release %d", got.Release)
	}
	h, _ := m.History(s.ID)
	res := map[int]string{}
	for _, d := range h {
		res[d.Release] = d.Result
	}
	if res[6] != "live" || res[5] != "ok" || res[4] != "ok" || res[3] != "removed" || res[2] != "failed" || res[1] != "removed" {
		t.Fatalf("results %v", res)
	}
	for n, want := range map[int]bool{6: true, 5: true, 4: true, 3: false, 2: false, 1: false} {
		_, err := os.Stat(m.releaseDir(s.ID, n))
		if (err == nil) != want {
			t.Errorf("release %d kept=%v", n, err == nil)
		}
	}
	// rollback to a kept release; refused for a removed, failed or live one
	if err := m.Rollback(context.Background(), s.ID, 4); err != nil {
		t.Fatal(err)
	}
	got, _ = m.Get(s.ID)
	h, _ = m.History(s.ID)
	if got.Release != 4 || h[findRelease(h, 4)].Result != "live" || !h[findRelease(h, 4)].Rollback || h[findRelease(h, 6)].Result != "ok" {
		t.Fatalf("after rollback %d %+v", got.Release, h)
	}
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	if !strings.Contains(string(cf), "./wp/releases/4:/app") {
		t.Error("compose not switched to release 4")
	}
	for _, n := range []int{4, 3, 2, 99} {
		if err := m.Rollback(context.Background(), s.ID, n); !errors.Is(err, ErrBadInput) {
			t.Errorf("rollback %d: %v", n, err)
		}
	}
}

func TestDeployOnlyNode(t *testing.T) {
	m, _, s, _ := laravelSite(t, "")
	if _, err := m.StartDeploy(s.ID); !errors.Is(err, ErrNotNode) {
		t.Fatal(err)
	}
	if _, err := m.History(s.ID); !errors.Is(err, ErrNotNode) {
		t.Fatal(err)
	}
	if err := m.Rollback(context.Background(), s.ID, 1); !errors.Is(err, ErrNotNode) {
		t.Fatal(err)
	}
	if _, err := m.BuildLog(s.ID, 1); !errors.Is(err, ErrNotNode) {
		t.Fatal(err)
	}
}

func TestYAMLQuote(t *testing.T) {
	if got := yamlQuote(`exec node "a\b" $HOME`); got != `"exec node \"a\\b\" $$HOME"` {
		t.Fatal(got)
	}
}

func TestNodeEnv(t *testing.T) {
	m, f, s, _ := nodeSite(t, okBuild, nil)
	ctx := context.Background()
	if err := m.SetEnv(ctx, s.ID, EnvChange{Set: map[string]string{"DATABASE_URL": "postgres://u:p@db/x?a=1#$HOME 'q' \"z\"", "NEXT_PUBLIC_API": "https://api"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(m.envPath(s.ID))
	if string(b) != "DATABASE_URL=postgres://u:p@db/x?a=1#$HOME 'q' \"z\"\nNEXT_PUBLIC_API=https://api\n" {
		t.Fatalf("env file %q", b)
	}
	if st, _ := os.Stat(m.envPath(s.ID)); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	names, _ := m.EnvNames(s.ID)
	if strings.Join(names, ",") != "DATABASE_URL,NEXT_PUBLIC_API" {
		t.Fatalf("names %v", names)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.Contains(last, "up -d --wait") || !strings.HasSuffix(last, " app") {
		t.Errorf("app not recreated: %s", last)
	}
	if err := m.SetEnv(ctx, s.ID, EnvChange{Delete: []string{"NEXT_PUBLIC_API"}, Set: map[string]string{"A_B": ""}}); err != nil {
		t.Fatal(err)
	}
	if names, _ = m.EnvNames(s.ID); strings.Join(names, ",") != "A_B,DATABASE_URL" {
		t.Fatalf("names %v", names)
	}
	for name, c := range map[string]EnvChange{
		"reserved": {Set: map[string]string{"PORT": "80"}},
		"digit":    {Set: map[string]string{"1A": "x"}},
		"dash":     {Set: map[string]string{"A-B": "x"}},
		"newline":  {Set: map[string]string{"A": "x\nB=y"}},
		"long":     {Set: map[string]string{"A": strings.Repeat("x", MaxEnvValue+1)}},
		"bad del":  {Delete: []string{"A=B"}},
	} {
		if err := m.SetEnv(ctx, s.ID, c); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the build container gets the values too
	if v := deployAndWait(t, m, s.ID); v.State != "done" {
		t.Fatal(v)
	}
	var build string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "run" {
			build = strings.Join(c, " ")
		}
	}
	if !strings.Contains(build, "--env-file "+m.envPath(s.ID)) {
		t.Errorf("build lacks the env file: %s", build)
	}
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	if !strings.Contains(string(cf), "format: raw") {
		t.Error("env file not read raw")
	}
	wp, _, _, _ := laravelSite(t, "")
	if _, err := wp.EnvNames("voltix-id"); !errors.Is(err, ErrNotNode) {
		t.Error(err)
	}
}
