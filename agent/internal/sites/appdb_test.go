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

func appDBSite(t *testing.T, c spec.Create) (*Manager, *fakeRunner, Site) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "run" && strings.Contains(j, "np-build-"):
			out, err := okBuild(1)
			return out, err, true
		case strings.Contains(j, " up -d --wait") && strings.HasSuffix(j, " app"):
			return nil, nil, true
		}
		return nil, nil, false
	}
	job, s, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	s, _ = m.Get(s.ID)
	return m, f, s
}

func TestAppDatabase(t *testing.T) {
	for _, c := range []struct {
		app, ver, engine, image, scheme, port, health string
	}{
		{"node", "24", "", catalog.MariaDB, "mysql://app:", "3306", "healthcheck.sh"},
		{"python", "3.14", spec.DBPostgres, catalog.Postgres, "postgres://app:", "5432", "pg_isready -q -h 127.0.0.1 -U app -d app"},
	} {
		m, f, s := appDBSite(t, spec.Create{App: c.app, Domain: "voltix.id", Primary: "domain", Version: c.ver, Source: "files", Database: true, DBEngine: c.engine})
		if !s.HasDB() || s.IsPostgres() != (c.engine == spec.DBPostgres) || s.DBEngine != c.engine {
			t.Fatalf("%s site %+v", c.app, s)
		}
		cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
		for _, want := range []string{"image: " + c.image, c.health, "db: { condition: service_healthy }", "networks: [nostek-edge, out, back]",
			"file: ./secrets/db_password", "./db:/var/lib/"} {
			if !strings.Contains(string(cf), want) {
				t.Errorf("%s compose lacks %q", c.app, want)
			}
		}
		pw, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "secrets", "db_password"))
		if len(pw) != 24 || strings.Contains(string(cf), string(pw)) {
			t.Fatalf("%s password file %q", c.app, pw)
		}
		env, _ := os.ReadFile(m.envPath(s.ID))
		if st, _ := os.Stat(m.envPath(s.ID)); st.Mode().Perm() != 0o600 {
			t.Errorf("%s env mode %v", c.app, st.Mode())
		}
		for _, want := range []string{"DATABASE_URL=" + c.scheme + string(pw) + "@db:" + c.port + "/app\n", "DB_HOST=db\n", "DB_PORT=" + c.port + "\n",
			"DB_NAME=app\n", "DB_USER=app\n", "DB_PASSWORD=" + string(pw) + "\n"} {
			if !strings.Contains(string(env), want) {
				t.Errorf("%s env lacks %q", c.app, want)
			}
		}
		if names, _ := m.EnvNames(s.ID); strings.Join(names, ",") != "DATABASE_URL,DB_HOST,DB_NAME,DB_PASSWORD,DB_PORT,DB_USER" {
			t.Errorf("%s env names %v", c.app, names)
		}
		pulled := ""
		for _, call := range f.calls {
			if len(call) > 0 && call[0] == "pull" {
				pulled += strings.Join(call, " ") + "\n"
			}
		}
		if !strings.Contains(pulled, c.image) {
			t.Errorf("%s db image not pulled: %s", c.app, pulled)
		}
		if strings.Join(services(s), ",") != "app,db" {
			t.Errorf("%s services %v", c.app, services(s))
		}
		// the database tools use the engine's own clients
		load, dump := strings.Join(m.loadArgs(s), " "), strings.Join(m.dumpArgs(s, true), " ")
		reset, rargs := m.resetArgs(s)
		if c.engine == spec.DBPostgres {
			if !strings.Contains(load, "psql -X -q -v ON_ERROR_STOP=1 -d app") || !strings.Contains(dump, "pg_dump --no-owner --no-acl --clean --if-exists -d app") ||
				!strings.Contains(reset, "DROP SCHEMA IF EXISTS public CASCADE") || !strings.Contains(strings.Join(rargs, " "), "psql") ||
				!strings.Contains(load, "PGPASSWORD=\"$(cat /run/secrets/db_password)\"") {
				t.Errorf("postgres tools: %s | %s | %s", load, dump, reset)
			}
		} else if !strings.Contains(load, "mariadb --batch") || !strings.Contains(dump, "mariadb-dump") || !strings.Contains(dump, "--routines") ||
			!strings.Contains(reset, "DROP DATABASE IF EXISTS app") {
			t.Errorf("mariadb tools: %s | %s | %s", load, dump, reset)
		}
		if strings.Contains(load+dump, string(pw)) {
			t.Error("password in the client arguments")
		}
	}
	// an app without a database has none, and no connection values
	m, _, s := appDBSite(t, spec.Create{App: "go", Domain: "voltix.id", Primary: "domain", Version: "1.27", Source: "files"})
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	if s.HasDB() || strings.Contains(string(cf), "db:") || fileExists(m.envPath(s.ID)) {
		t.Error("database made for an app without one")
	}
}

func TestParsePGHTML(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "psql16.html"))
	if err != nil {
		t.Fatal(err)
	}
	res := parsePGHTML(string(b), "abc", "NP-NULL-x")
	if len(res) != 2 {
		t.Fatalf("results %d", len(res))
	}
	r := res[0]
	if strings.Join(r.Columns, ",") != "id,name,note" || len(r.Rows) != 2 {
		t.Fatalf("result %+v", r)
	}
	if *r.Rows[0][1] != `a <b> & "c"` || r.Rows[0][2] != nil || *r.Rows[1][1] != "" || *r.Rows[1][2] != "line1\nline2" {
		t.Errorf("cells %q %v %q %q", *r.Rows[0][1], r.Rows[0][2], *r.Rows[1][1], *r.Rows[1][2])
	}
	// text that looks like a result table is a cell, never a table of its own
	if len(res[1].Rows) != 1 || *res[1].Rows[0][0] != `x <table class="np-abc">` {
		t.Errorf("trick %+v", res[1])
	}
	if len(parsePGHTML(string(b), "zzz", "NP-NULL-x")) != 0 {
		t.Error("tables without the marker read")
	}
	var big strings.Builder
	big.WriteString(`<table border="1" class="np-m"><tr><th align="center">n</th></tr>`)
	for i := 0; i < QueryRows+5; i++ {
		big.WriteString(`<tr valign="top"><td align="right">1</td></tr>`)
	}
	big.WriteString("</table>")
	if r := parsePGHTML(big.String(), "m", "N"); len(r) != 1 || len(r[0].Rows) != QueryRows || !r[0].Limited {
		t.Errorf("limit: %d %v", len(r[0].Rows), r[0].Limited)
	}
	if e := pgErr([]byte("ERROR:  syntax error at or near \"SELEC\"\nLINE 1: SELEC 1;\n")); e == nil || !strings.Contains(e.Error(), `syntax error at or near "SELEC"`) {
		t.Errorf("error %v", e)
	}
}
