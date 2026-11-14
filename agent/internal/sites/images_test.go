package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const oldDB = "mariadb:11.8@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// olderSite is a ready WordPress site whose database container still runs an image of an older agent.
func olderSite(t *testing.T) (*Manager, *fakeRunner, string, string) {
	t.Helper()
	m, f, id := readySite(t)
	ticking(m, time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	p, _ := catalog.PHP("8.4")
	cf, _ := os.ReadFile(m.composePath(id))
	old := strings.Replace(string(cf), "image: "+catalog.MariaDB, "image: "+oldDB, 1)
	if old == string(cf) {
		t.Fatal("no MariaDB image in compose")
	}
	if err := os.WriteFile(m.composePath(id), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	f.ps = `{"Service":"db","Image":"` + oldDB + `","State":"running","Health":"healthy"}` + "\n" +
		`{"Service":"web","Image":"` + p.Apache + `","State":"running","Health":"healthy"}`
	return m, f, id, old
}

func calls(f *fakeRunner) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []string
	for _, c := range f.calls {
		all = append(all, strings.Join(c, " "))
	}
	return strings.Join(all, "\n")
}

func TestImageStatus(t *testing.T) {
	m, f, id, _ := olderSite(t)
	img, err := m.ImageStatus(context.Background(), id)
	if err != nil || !img.Update {
		t.Fatalf("status %+v %v", img, err)
	}
	got := map[string]ImageUse{}
	for _, u := range img.Services {
		got[u.Service] = u
	}
	if u := got["db"]; !u.Update || u.Current != oldDB || u.Wanted != catalog.MariaDB {
		t.Errorf("db %+v", u)
	}
	if u := got["web"]; u.Update {
		t.Errorf("web %+v", u)
	}
	// the WP-CLI container has no running container: the compose file tells its image
	if u := got["cli"]; u.Update || u.Current != catalog.CLI {
		t.Errorf("cli %+v", u)
	}
	// a container keeps its image after the file was rewritten: the running one counts
	f.ps = strings.Replace(f.ps, oldDB, "sha256:abcdef", 1)
	if img, _ := m.ImageStatus(context.Background(), id); !img.Update {
		t.Error("container on an unnamed image not seen as old")
	}
	f.ps = ""
	_ = os.WriteFile(m.composePath(id), []byte(strings.Replace(mustRead(t, m.composePath(id)), oldDB, catalog.MariaDB, 1)), 0o644)
	if img, _ := m.ImageStatus(context.Background(), id); img.Update {
		t.Errorf("up to date reported as old: %+v", img)
	}
	if _, err := m.StartImageUpdate(id); !errors.Is(err, ErrBadInput) {
		t.Errorf("update with nothing to do: %v", err)
	}
	if _, err := m.ImageStatus(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site: %v", err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRewriteKeepsImages(t *testing.T) {
	m, _, id, old := olderSite(t)
	s, _ := m.Get(id)
	// a new agent renders the file differently: the rest changes, the images stay
	_ = os.WriteFile(m.composePath(id), []byte(strings.Replace(old, "pids_limit: 512", "pids_limit: 100", 1)), 0o644)
	if err := m.syncCompose(s); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, m.composePath(id)); got != old {
		t.Errorf("syncCompose changed images or kept the old text:\n%s", got)
	}
	cf := "services:\n  db:\n    image: new-db\n  app:\n    image: new-app\nnetworks:\n  back:\n    image: not-a-service\n"
	prev := "services:\n  db:\n    image: old-db\n  app:\n    image: old-app\n"
	want := "services:\n  db:\n    image: old-db\n  app:\n    image: new-app\nnetworks:\n  back:\n    image: not-a-service\n"
	if got := keepImages(cf, prev, "app"); got != want {
		t.Errorf("keepImages:\n%s", got)
	}
	if got := keepImages(cf, ""); got != cf {
		t.Errorf("keepImages without an old file:\n%s", got)
	}
}

func TestImageUpdate(t *testing.T) {
	m, f, id, _ := olderSite(t)
	f.dumped = "-- dump\n"
	job, err := m.StartImageUpdate(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || v.Kind != "image" {
		t.Fatalf("job %+v", v)
	}
	all := calls(f)
	pull := strings.Index(all, "pull --quiet "+catalog.MariaDB)
	up := strings.LastIndex(all, "up -d --wait")
	if pull < 0 || up < pull || strings.Count(all, "\npull --quiet ") != 1 {
		t.Errorf("pull/up order:\n%s", all)
	}
	if !strings.Contains(mustRead(t, m.composePath(id)), "image: "+catalog.MariaDB+"\n") {
		t.Error("compose not on the new image")
	}
	b, _ := m.ListBackups(id)
	if len(b.Backups) != 1 || b.Backups[0].Kind != "before-update" {
		t.Errorf("backups %+v", b.Backups)
	}
}

func TestImageUpdateFailures(t *testing.T) {
	// a failed pull changes nothing, not even a backup
	m, f, id, old := olderSite(t)
	f.fail = "pull"
	job, _ := m.StartImageUpdate(id)
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "nothing was changed") {
		t.Fatalf("job %+v", v)
	}
	if b, _ := m.ListBackups(id); len(b.Backups) != 0 || mustRead(t, m.composePath(id)) != old {
		t.Error("a failed pull changed the site")
	}
	// containers that do not come up healthy: the previous file is put back and started
	m, f, id, old = olderSite(t)
	f.dumped = "-- dump\n"
	ups := 0
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d --wait") {
			ups++
			if ups == 1 {
				return []byte("db is unhealthy"), errors.New("exit 1"), true
			}
		}
		return nil, nil, false
	}
	job, _ = m.StartImageUpdate(id)
	v := waitJob(t, m, job)
	if v.State != "failed" || !strings.Contains(v.Error, "previous images again") || !strings.Contains(v.Error, "unhealthy") || ups != 2 {
		t.Fatalf("job %+v ups=%d", v, ups)
	}
	if mustRead(t, m.composePath(id)) != old {
		t.Error("previous compose file not put back")
	}
	// neither starts: the owner is told which backup to restore
	m, f, id, _ = olderSite(t)
	f.dumped = "-- dump\n"
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d --wait") {
			return []byte("broken"), errors.New("exit 1"), true
		}
		return nil, nil, false
	}
	job, _ = m.StartImageUpdate(id)
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "restore the backup before-update-") {
		t.Fatalf("job %+v", v)
	}
}

func TestImageUpdateBuiltImage(t *testing.T) {
	m, f, s, _ := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4", Database: true, DBEngine: spec.DBPostgres})
	ticking(m, time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	p, _ := catalog.PHP("8.4")
	img := catalog.PHPPostgresImage(p, "nginx")
	cf := mustRead(t, m.composePath(s.ID))
	_ = os.WriteFile(m.composePath(s.ID), []byte(strings.Replace(cf, img, "nostek-local/php-pg:php8.4-fpm-000000000000", 1)), 0o644)
	_ = os.WriteFile(m.recipePath(s.ID), []byte("FROM old\n"), 0o644)
	f.ps = ""
	f.calls = nil
	job, err := m.StartImageUpdate(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || !strings.Contains(strings.Join(v.Steps, ","), "build image") {
		t.Fatalf("job %+v", v)
	}
	all := calls(f)
	if strings.Contains(all, "pull --quiet nostek-local/") || !strings.Contains(all, "build --quiet php") {
		t.Errorf("calls:\n%s", all)
	}
	if mustRead(t, m.recipePath(s.ID)) != catalog.PHPPostgresDockerfile(p.FPM) || !strings.Contains(mustRead(t, m.composePath(s.ID)), "image: "+img+"\n") {
		t.Error("recipe or compose not updated")
	}
	if _, err := os.Stat(filepath.Join(m.dir(s.ID), "build")); err != nil {
		t.Error(err)
	}
}

func TestDeployKeepsDatabaseImage(t *testing.T) {
	m, _, s := appDBSite(t, spec.Create{App: "node", Domain: "voltix.id", Primary: "domain", Version: "24", Source: "files", Database: true})
	cf := mustRead(t, m.composePath(s.ID))
	rt, _ := catalog.RuntimeOf("node", "24")
	old := strings.Replace(strings.Replace(cf, "image: "+catalog.MariaDB, "image: "+oldDB, 1), "image: "+rt.Image, "image: node:old", 1)
	if old == cf || !strings.Contains(old, "node:old") {
		t.Fatal("images not found in compose")
	}
	_ = os.WriteFile(m.composePath(s.ID), []byte(old), 0o644)
	if _, err := m.switchRelease(context.Background(), &s, s.Release); err != nil {
		t.Fatal(err)
	}
	got := imageMap(mustRead(t, m.composePath(s.ID)))
	if got["db"] != oldDB || got["app"] != rt.Image {
		t.Errorf("after a deploy: %v", got)
	}
}
