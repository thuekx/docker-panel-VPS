package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestPHPOnPostgres(t *testing.T) {
	p, _ := catalog.PHP("8.4")
	for _, c := range []struct{ server, base, build string }{{"apache", p.Apache, "web"}, {"nginx", p.FPM, "php"}} {
		m, f, s, v := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: c.server, PHP: "8.4", Database: true, DBEngine: spec.DBPostgres})
		if v.State != "done" || !strings.Contains(strings.Join(v.Steps, ","), "build PHP image") {
			t.Fatalf("%s job %+v", c.server, v)
		}
		got, _ := m.Get(s.ID)
		if !got.IsPostgres() {
			t.Fatalf("%s site %+v", c.server, got)
		}
		cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
		comp := string(cf)
		img := catalog.PHPPostgresImage(p, c.server)
		for _, want := range []string{"image: " + catalog.Postgres, "image: " + img + "\n    build:\n      context: ./build\n    pull_policy: never",
			`DB_PORT: "5432"`, "DB_ENGINE: postgres", "DB_PASSWORD_FILE: /run/secrets/db_password"} {
			if !strings.Contains(comp, want) {
				t.Errorf("%s compose lacks %q:\n%s", c.server, want, comp)
			}
		}
		if strings.Contains(comp, "MARIADB") || strings.Contains(comp, "image: "+c.base) {
			t.Errorf("%s: MariaDB or the plain image in compose", c.server)
		}
		df, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "build", "Dockerfile"))
		if string(df) != catalog.PHPPostgresDockerfile(c.base) || !strings.Contains(string(df), "FROM "+c.base+"\n") || !strings.Contains(string(df), "pdo_pgsql") {
			t.Errorf("%s Dockerfile:\n%s", c.server, df)
		}
		built := false
		for _, call := range f.calls {
			if j := strings.Join(call, " "); strings.HasSuffix(j, "build --quiet "+c.build) {
				built = true
			}
		}
		if !built {
			t.Errorf("%s: %s image not built", c.server, c.build)
		}
		idx, _ := os.ReadFile(filepath.Join(m.wpDir(s.ID), "index.php"))
		if !strings.Contains(string(idx), "pgsql:host=") {
			t.Errorf("%s starter has no PostgreSQL check", c.server)
		}
		if load := strings.Join(m.loadArgs(got), " "); !strings.Contains(load, "psql") {
			t.Errorf("%s tools: %s", c.server, load)
		}
	}
	// MariaDB PHP sites keep the pulled official image
	m, _, s, _ := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: "apache", PHP: "8.4", Database: true})
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	if !strings.Contains(string(cf), "image: "+p.Apache) || strings.Contains(string(cf), "build:") || strings.Contains(string(cf), "DB_ENGINE") || fileExists(filepath.Join(m.dir(s.ID), "build")) {
		t.Error("MariaDB PHP site changed")
	}
}

func TestLaravelOnPostgres(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "composer create-project") {
			_ = os.WriteFile(filepath.Join(m.wpDir("voltix-id"), ".env.example"), []byte(envExample), 0o644)
			return nil, nil, true
		}
		return nil, nil, false
	}
	job, s, err := m.Create(context.Background(), spec.Create{App: "laravel", Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4", DBEngine: spec.DBPostgres})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	s, _ = m.Get(s.ID)
	p, _ := catalog.PHP("8.4")
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	for _, want := range []string{"DB_CONNECTION: pgsql", `DB_PORT: "5432"`, "image: " + catalog.LaravelPostgresImage(p), "image: " + catalog.Postgres} {
		if !strings.Contains(string(cf), want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	if strings.Contains(string(cf), catalog.LaravelImage(p)+"\n") || strings.Contains(string(cf), "MARIADB") {
		t.Error("MariaDB image or db in a PostgreSQL Laravel")
	}
	df, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "build", "Dockerfile"))
	if string(df) != catalog.LaravelPostgresDockerfile(p) || !strings.Contains(string(df), "pdo_mysql") || !strings.Contains(string(df), "pdo_pgsql") {
		t.Errorf("Dockerfile:\n%s", df)
	}
	env, _ := os.ReadFile(filepath.Join(m.wpDir(s.ID), ".env"))
	if !strings.Contains(string(env), "DB_CONNECTION=pgsql\n") || !strings.Contains(string(env), "DB_PORT=5432\n") || !strings.Contains(string(env), "own PostgreSQL") {
		t.Errorf(".env:\n%s", env)
	}
	// the MariaDB recipe and tag stay as they were, so existing Laravel sites keep their image
	if catalog.LaravelImage(p) == catalog.LaravelPostgresImage(p) || strings.Contains(catalog.LaravelDockerfile(p), "pgsql") {
		t.Error("MariaDB Laravel recipe changed")
	}
	if _, err := m.Plan(context.Background(), spec.Create{App: "static", Domain: "voltix.id", Primary: "domain", Server: "nginx", DBEngine: spec.DBPostgres}); !errors.Is(err, ErrBadInput) && err == nil {
		t.Error("static on PostgreSQL planned")
	}
}
