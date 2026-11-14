package sites

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestReadyApps(t *testing.T) {
	for _, a := range catalog.ReadyAll() {
		m, f, s, v := appSite(t, spec.Create{App: a.ID, Domain: "voltix.id", Primary: "domain"})
		if v.State != "done" {
			t.Fatalf("%s job %+v", a.ID, v)
		}
		got, _ := m.Get(s.ID)
		if got.Phase != "ready" || !got.IsReady() || !got.OneApp() || got.HasDB() {
			t.Fatalf("%s site %+v", a.ID, got)
		}
		comp := mustRead(t, m.composePath(s.ID))
		for _, want := range []string{"  app:\n", "image: " + a.Image + "\n", `user: "33:33"`, "- ./wp:" + a.Data + "\n",
			`loadbalancer.server.port: "` + itoa(a.Port) + `"`, "networks: [nostek-edge, out]", "Host(`voltix.id`)"} {
			if !strings.Contains(comp, want) {
				t.Errorf("%s compose lacks %q:\n%s", a.ID, want, comp)
			}
		}
		if strings.Contains(comp, "  web:") || strings.Contains(comp, "  db:") || strings.Contains(comp, "ports:") {
			t.Errorf("%s: extra service or published port:\n%s", a.ID, comp)
		}
		for _, f := range []string{"php.ini", "nginx.conf", filepath.Join("wp", "index.php"), filepath.Join("wp", "index.html")} {
			if _, err := os.Stat(filepath.Join(m.dir(s.ID), f)); err == nil {
				t.Errorf("%s: %s written", a.ID, f)
			}
		}
		if fi, err := os.Stat(m.wpDir(s.ID)); err != nil || !fi.IsDir() {
			t.Errorf("%s: no data folder", a.ID)
		}
		pulled := false
		for _, c := range f.calls {
			if strings.Contains(strings.Join(c, " "), "pull --quiet") {
				pulled = true
			}
		}
		if !pulled {
			t.Errorf("%s: image not pulled", a.ID)
		}
		if !validService(got, "app") || validService(got, "web") {
			t.Errorf("%s services %v", a.ID, services(got))
		}
	}
	m, _, s, _ := appSite(t, spec.Create{App: "n8n", Domain: "voltix.id", Primary: "domain"})
	comp := mustRead(t, m.composePath(s.ID))
	for _, want := range []string{`N8N_HOST: "voltix.id"`, `WEBHOOK_URL: "https://voltix.id/"`, `N8N_PROTOCOL: "https"`, `N8N_USER_FOLDER: "/data"`} {
		if !strings.Contains(comp, want) {
			t.Errorf("n8n compose lacks %q", want)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestReadyAppRefusals(t *testing.T) {
	base := spec.Create{App: "uptime-kuma", Domain: "voltix.id", Primary: "domain"}
	for name, c := range map[string]spec.Create{
		"server":   {App: "uptime-kuma", Domain: "voltix.id", Primary: "domain", Server: "nginx"},
		"php":      {App: "n8n", Domain: "voltix.id", Primary: "domain", PHP: "8.4"},
		"version":  {App: "n8n", Domain: "voltix.id", Primary: "domain", Version: "2"},
		"database": {App: "n8n", Domain: "voltix.id", Primary: "domain", Database: true},
		"engine":   {App: "n8n", Domain: "voltix.id", Primary: "domain", DBEngine: spec.DBPostgres},
		"repo":     {App: "n8n", Domain: "voltix.id", Primary: "domain", Repo: "https://github.com/a/b.git"},
		"email":    {App: "uptime-kuma", Domain: "voltix.id", Primary: "domain", AdminEmail: "a@b.c"},
		"unknown":  {App: "ghost", Domain: "voltix.id", Primary: "domain"},
	} {
		if err := c.Check(nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := base.Check(nil); err != nil {
		t.Errorf("plain ready app refused: %v", err)
	}
}

func TestReadyAppBackupStopsApp(t *testing.T) {
	m, f, s, _ := appSite(t, spec.Create{App: "uptime-kuma", Domain: "voltix.id", Primary: "domain"})
	ticking(m, time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	_ = os.WriteFile(filepath.Join(m.wpDir(s.ID), "kuma.db"), []byte("SQLite"), 0o644)
	f.calls = nil
	job, err := m.StartBackup(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("backup %+v", v)
	}
	all := calls(f)
	stop, start := strings.Index(all, "stop app"), strings.Index(all, "start app")
	if stop < 0 || start < stop {
		t.Errorf("app not stopped around the copy:\n%s", all)
	}
	b, _ := m.ListBackups(s.ID)
	if len(b.Backups) != 1 || !strings.Contains(strings.Join(entryNames(t, filepath.Join(m.backupDir(s.ID), b.Backups[0].Name)), " "), "wp/kuma.db") {
		t.Errorf("backup %+v", b.Backups)
	}
	if _, err := m.Logs(context.Background(), s.ID, "app", 10); err != nil {
		t.Errorf("logs of the app: %v", err)
	}
}

func entryNames(t *testing.T, p string) []string {
	var n []string
	for k := range entries(t, p) {
		n = append(n, k)
	}
	return n
}
