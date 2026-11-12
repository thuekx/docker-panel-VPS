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

const envExample = `APP_NAME=Laravel
APP_ENV=local
APP_KEY=
APP_DEBUG=true
APP_URL=http://localhost

DB_CONNECTION=sqlite
# DB_HOST=127.0.0.1
# DB_PORT=3306
# DB_DATABASE=laravel
# DB_USERNAME=root
# DB_PASSWORD=

QUEUE_CONNECTION=database
`

func laravelSite(t *testing.T, failOn string) (*Manager, *fakeRunner, Site, JobView) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	f.fail = failOn
	f.hook = func(args []string) ([]byte, error, bool) {
		if failOn != "" && strings.Contains(strings.Join(args, " "), failOn) {
			return []byte("boom"), errors.New("exit 1"), true
		}
		if strings.Contains(strings.Join(args, " "), "composer create-project") {
			// what composer leaves in the website folder
			wp := m.wpDir("voltix-id")
			if err := os.WriteFile(filepath.Join(wp, ".env.example"), []byte(envExample), 0o644); err != nil {
				return nil, err, true
			}
			return []byte("Installing laravel/laravel"), nil, true
		}
		return nil, nil, false
	}
	job, s, err := m.Create(context.Background(), spec.Create{App: "laravel", Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4", Title: "Voltix Shop"})
	if err != nil {
		t.Fatal(err)
	}
	return m, f, s, waitJob(t, m, job)
}

func TestLaravelSite(t *testing.T) {
	m, f, s, v := laravelSite(t, "")
	if v.State != "done" || strings.Join(v.Steps, ",") != "files,build PHP image,pull images,install Laravel,start containers" || v.HasSecret {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if !got.IsLaravel() || !got.HasDB() || got.DBName() != "app" || got.AdminUser != "" || got.Phase != "ready" {
		t.Fatalf("site %+v", got)
	}
	dir := m.dir(s.ID)
	cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	p, _ := catalog.PHP("8.4")
	for _, want := range []string{
		"image: " + catalog.LaravelImage(p), "context: ./build", `user: "33:33"`, "queue:work", "schedule:work",
		"DB_PASSWORD_FILE: /run/secrets/db_password", "zz-nostek-laravel.ini", "MARIADB_DATABASE: app",
	} {
		if !strings.Contains(string(cf), want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	for _, bad := range []string{"DB_PASSWORD:", "WORDPRESS", "cli:", "apache"} {
		if strings.Contains(string(cf), bad) {
			t.Errorf("compose holds %q", bad)
		}
	}
	if df, _ := os.ReadFile(filepath.Join(dir, "build", "Dockerfile")); string(df) != catalog.LaravelDockerfile(p) {
		t.Error("build recipe not written")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "build")); len(ents) != 1 {
		t.Errorf("build context holds more than the recipe: %v", ents)
	}
	nc, _ := os.ReadFile(filepath.Join(dir, "nginx.conf"))
	if !strings.Contains(string(nc), "root /var/www/html/public;") {
		t.Error("nginx does not serve public/")
	}
	ini, _ := os.ReadFile(filepath.Join(dir, "laravel.ini"))
	pre, _ := os.ReadFile(filepath.Join(dir, "laravel-env.php"))
	if !strings.Contains(string(ini), "auto_prepend_file") || !strings.Contains(string(pre), "DB_PASSWORD_FILE") {
		t.Error("password hand-over files missing")
	}
	env, err := os.ReadFile(filepath.Join(m.wpDir(s.ID), ".env"))
	if err != nil {
		t.Fatal(err)
	}
	e := string(env)
	for _, want := range []string{`APP_NAME="Voltix Shop"`, "APP_ENV=production", "APP_DEBUG=false", "APP_URL=" + got.URL,
		"DB_CONNECTION=mysql", "DB_HOST=db", "DB_DATABASE=app", "DB_USERNAME=app", "APP_KEY=", "QUEUE_CONNECTION=database"} {
		if !strings.Contains(e, want) {
			t.Errorf(".env lacks %q:\n%s", want, e)
		}
	}
	for _, bad := range []string{"DB_PASSWORD=", "APP_ENV=local", "sqlite", "127.0.0.1"} {
		if strings.Contains(e, bad) {
			t.Errorf(".env holds %q", bad)
		}
	}
	if st, _ := os.Stat(filepath.Join(m.wpDir(s.ID), ".env")); st.Mode().Perm() != 0o640 {
		t.Errorf(".env mode %v", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(m.wpDir(s.ID), "index.html")); err == nil {
		t.Error("starter page written into the Laravel folder")
	}
	var seen []string
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "artisan") || strings.Contains(j, "composer") || strings.Contains(j, " build ") || strings.Contains(j, " pull ") {
			seen = append(seen, j[strings.Index(j, " compose ")+1:])
		}
		pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "db_password"))
		if strings.Contains(j, strings.TrimSpace(string(pw))) {
			t.Fatal("database password on a command line")
		}
	}
	all := strings.Join(seen, "\n")
	if got.Version != "13" {
		t.Errorf("version %q", got.Version)
	}
	for _, want := range []string{"build --quiet php", "--ignore-buildable", "composer create-project", "laravel/laravel:^13.0", "key:generate", "package:discover", "storage:link", "migrate --force"} {
		if !strings.Contains(all, want) {
			t.Errorf("create did not run %q:\n%s", want, all)
		}
	}
	if strings.Index(all, "key:generate") < strings.Index(all, "create-project") || strings.Index(all, "migrate") < strings.Index(all, "key:generate") {
		t.Errorf("install order:\n%s", all)
	}
	if got := services(got); strings.Join(got, ",") != "web,php,db,queue,scheduler" {
		t.Errorf("services %v", got)
	}
	if _, err := m.WordPress(context.Background(), s.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("WordPress tools on Laravel: %v", err)
	}
	// artisan: listed commands only, Laravel only
	f.answer = map[string]string{"artisan migrate": "Nothing to migrate."}
	if out, err := m.Artisan(context.Background(), s.ID, "migrate"); err != nil || out != "Nothing to migrate." {
		t.Fatalf("artisan migrate: %q %v", out, err)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.Contains(last, "exec -T php php artisan migrate --force") {
		t.Errorf("artisan call %q", last)
	}
	for _, op := range []string{"db:wipe", "tinker", "migrate --seed", ""} {
		if _, err := m.Artisan(context.Background(), s.ID, op); !errors.Is(err, ErrBadInput) {
			t.Errorf("artisan %q accepted", op)
		}
	}
}

func TestLaravelInstallFails(t *testing.T) {
	m, _, s, v := laravelSite(t, "create-project")
	if v.State != "failed" || !strings.Contains(v.Error, "install Laravel") {
		t.Fatalf("job %+v", v)
	}
	if got, _ := m.Get(s.ID); got.Phase != "failed" {
		t.Fatalf("phase %s", got.Phase)
	}
}

func TestArtisanOnOtherApps(t *testing.T) {
	m, _, s, _ := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4"})
	if _, err := m.Artisan(context.Background(), s.ID, "migrate"); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "does not run Laravel") {
		t.Errorf("artisan on a PHP site: %v", err)
	}
}

func TestLaravelEnvName(t *testing.T) {
	s := Site{Domain: "voltix.id", URL: "https://voltix.id", Title: `Bad "name" $(x)`}
	if e := string(laravelEnv([]byte(envExample), s)); !strings.Contains(e, `APP_NAME="voltix.id"`) || strings.Contains(e, "$(x)") {
		t.Errorf("unsafe title reached .env:\n%s", e)
	}
}

func TestLaravelVersionChoice(t *testing.T) {
	for _, c := range []struct {
		in   spec.Create
		want string
	}{
		{spec.Create{App: "laravel"}, "13"},
		{spec.Create{App: "laravel", Version: "12"}, "12"},
		{spec.Create{App: "php", Version: "12"}, ""},
		{spec.Create{}, ""},
	} {
		if got := laravelVersion(c.in); got != c.want {
			t.Errorf("%+v: %q", c.in, got)
		}
	}
}
