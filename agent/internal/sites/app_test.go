package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func appSite(t *testing.T, c spec.Create) (*Manager, *fakeRunner, Site, JobView) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	job, s, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return m, f, s, waitJob(t, m, job)
}

func TestStaticSite(t *testing.T) {
	m, f, s, v := appSite(t, spec.Create{App: "static", Domain: "voltix.id", Primary: "domain", Server: "nginx", Title: "Voltix & Co"})
	if v.State != "done" || strings.Join(v.Steps, ",") != "files,pull images,start containers" || v.HasSecret {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if got.App != "static" || got.Phase != "ready" || got.AdminUser != "" || got.HasDB() || got.IsWP() {
		t.Fatalf("site %+v", got)
	}
	idx, err := os.ReadFile(filepath.Join(m.wpDir(s.ID), "index.html"))
	if err != nil || !strings.Contains(string(idx), "Voltix &amp; Co") || strings.Contains(string(idx), "<?php") {
		t.Fatalf("starter page: %v %s", err, idx)
	}
	if _, err := os.Stat(filepath.Join(m.dir(s.ID), "db")); err == nil {
		t.Error("static site got a database folder")
	}
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	nc, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "nginx.conf"))
	for _, bad := range []string{"mariadb", "db_password", "cli:", "php", "WORDPRESS"} {
		if strings.Contains(string(cf), bad) {
			t.Errorf("static compose holds %q", bad)
		}
	}
	if !strings.Contains(string(cf), "./wp:/var/www/html:ro") || !strings.Contains(string(nc), "try_files $uri $uri/ /index.html") {
		t.Error("static site not served read-only with SPA fallback")
	}
	if _, err := os.Stat(filepath.Join(m.dir(s.ID), "php.ini")); err == nil {
		t.Error("static site got a php.ini")
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), " wp ") {
			t.Fatal("WP-CLI ran for a static site")
		}
	}
	if got := services(got); strings.Join(got, ",") != "web" {
		t.Errorf("services %v", got)
	}
	if _, err := m.Database(context.Background(), s.ID); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "no database") {
		t.Errorf("database of a static site: %v", err)
	}
	if _, err := m.WordPress(context.Background(), s.ID); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "does not run WordPress") {
		t.Errorf("WordPress tools on a static site: %v", err)
	}
	// backup and restore work without a database
	job, err := m.StartBackup(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || strings.Contains(strings.Join(v.Steps, ","), "database") {
		t.Fatalf("backup %+v", v)
	}
	b, _ := m.ListBackups(s.ID)
	ents := entries(t, filepath.Join(m.backupDir(s.ID), b.Backups[0].Name))
	if _, ok := ents["database.sql"]; ok || ents["wp/index.html"] == "" {
		t.Fatalf("archive %v", ents)
	}
	if _, err := m.StartRestore(s.ID, b.Backups[0].Name, "database"); !errors.Is(err, ErrBadInput) {
		t.Error("database restore of a site without database accepted")
	}
	_ = os.WriteFile(filepath.Join(m.wpDir(s.ID), "index.html"), []byte("changed"), 0o644)
	job, _ = m.StartRestore(s.ID, b.Backups[0].Name, "all")
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("restore %+v", v)
	}
	if idx2, _ := os.ReadFile(filepath.Join(m.wpDir(s.ID), "index.html")); string(idx2) != string(idx) {
		t.Error("files not restored")
	}
}

func TestPHPSite(t *testing.T) {
	for _, c := range []struct {
		server string
		db     bool
	}{{"apache", true}, {"nginx", false}, {"nginx", true}} {
		m, _, s, v := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: c.server, PHP: "8.3", Database: c.db})
		if v.State != "done" {
			t.Fatalf("%v: job %+v", c, v)
		}
		got, _ := m.Get(s.ID)
		cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
		comp := string(cf)
		if !strings.Contains(comp, `entrypoint: ["docker-php-entrypoint"]`) || strings.Contains(comp, "WORDPRESS_") || strings.Contains(comp, "cli:") {
			t.Errorf("%v: PHP app could copy WordPress in:\n%s", c, comp)
		}
		if c.db != strings.Contains(comp, "MARIADB_DATABASE: app") || c.db != strings.Contains(comp, "DB_PASSWORD_FILE: /run/secrets/db_password") || c.db != got.HasDB() {
			t.Errorf("%v: database wiring wrong", c)
		}
		if strings.Contains(comp, "DB_PASSWORD:") {
			t.Errorf("%v: password as an environment value", c)
		}
		idx, err := os.ReadFile(filepath.Join(m.wpDir(s.ID), "index.php"))
		if err != nil || !strings.Contains(string(idx), "voltix.id") {
			t.Errorf("%v: starter %v", c, err)
		}
		want := "web"
		if c.server == "nginx" {
			want += ",php"
			nc, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "nginx.conf"))
			if !strings.Contains(string(nc), "/index.php?$query_string") || strings.Contains(string(nc), "wp-config") {
				t.Errorf("%v: nginx conf", c)
			}
		}
		if c.db {
			want += ",db"
		}
		if g := strings.Join(services(got), ","); g != want {
			t.Errorf("%v: services %s", c, g)
		}
	}
}

func TestPHPSiteDatabaseName(t *testing.T) {
	m, f, s, _ := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: "apache", PHP: "8.4", Database: true})
	f.answer = map[string]string{"--skip-column-names": "11.8.3-MariaDB\n"}
	d, err := m.Database(context.Background(), s.ID)
	if err != nil || d.Name != "app" || d.User != "app" {
		t.Fatalf("db %+v %v", d, err)
	}
	if last := strings.Join(f.calls[len(f.calls)-1], " "); !strings.HasSuffix(last, " app") {
		t.Errorf("client call: %s", last)
	}
}
