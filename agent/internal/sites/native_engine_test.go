package sites

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

type fakeHost struct {
	mu     sync.Mutex
	calls  []string
	stdin  []string
	active map[string]bool
	fail   string
}

func (f *fakeHost) Host(_ context.Context, stdin io.Reader, bin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := hostBins[bin]; !ok {
		return nil, errors.New("not allowed")
	}
	line := bin + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, string(b))
	}
	if f.fail != "" && strings.Contains(line, f.fail) {
		return []byte("boom"), errors.New("exit 1")
	}
	if f.active == nil {
		f.active = map[string]bool{}
	}
	if bin == "systemctl" && len(args) == 2 {
		switch args[0] {
		case "start", "restart":
			f.active[args[1]] = true
		case "stop":
			f.active[args[1]] = false
		case "is-active":
			if f.active[args[1]] {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("exit 3")
		}
	}
	if bin == "systemctl" && len(args) == 3 && args[0] == "enable" {
		f.active[args[2]] = true
	}
	return nil, nil
}

func (f *fakeHost) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

var testNative = &NativeConfig{Gateway: "172.18.0.1", Port: NativePort, PHP: []string{"8.3", "8.4"}, MariaDBSocket: "/run/mysqld/mysqld.sock", WPCLI: "/usr/local/bin/wp"}

func nativeTest(t *testing.T) (*Manager, *fakeRunner, *fakeHost, NativePaths) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}, "www.voltix.id": {ip}})
	h := &fakeHost{}
	root := t.TempDir()
	p := NativePaths{Etc: filepath.Join(root, "etc"), Run: filepath.Join(root, "run"), Data: m.cfg.Data}
	m.EnableNative(testNative, h, p)
	m.lookupUser = func(string) (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	return m, f, h, p
}

func nativeReq(app string) spec.Create {
	c := req()
	c.Engine, c.Server, c.App = spec.EngineNative, "nginx", app
	if app == spec.AppStatic {
		c.PHP = ""
	}
	if app != "" && app != spec.AppWordPress {
		c.AdminEmail, c.Title = "", ""
	}
	return c
}

func TestNativeWordPress(t *testing.T) {
	m, f, h, p := nativeTest(t)
	job, s, err := m.Create(context.Background(), nativeReq(""))
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || !v.HasSecret {
		t.Fatalf("create %+v\n%s", v, h.all())
	}
	got, _ := m.Get(s.ID)
	if !got.IsNative() || got.Phase != "ready" {
		t.Errorf("site %+v", got)
	}
	all := h.all()
	user := NativeUser(s.ID)
	for _, want := range []string{
		"useradd --system --user-group --no-create-home",
		"usermod --append --groups " + user + " www-data",
		"systemctl enable --now " + NativeUnit(s.ID),
		"nginx -t -q\nsystemctl reload nginx.service",
		"--uid=" + user + " --gid=" + user,
		"-p ProtectSystem=strict",
		"-p TemporaryFileSystem=" + m.cfg.Data + ":ro",
		"-- /usr/bin/php8.4 /usr/local/bin/wp --path=" + p.docroot(s.ID) + " core download --version=7.1.4",
		"config create --dbname=" + NativeDB(s.ID),
		"--dbhost=localhost:/run/mysqld/mysqld.sock --prompt=dbpass",
		"core install --url=https://voltix.id",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "IDENTIFIED BY") || strings.Contains(all, v(t, m, job)) || !strings.Contains(strings.Join(h.stdin, ""), "\n") {
		t.Error("a password reached a command line")
	}
	sql := strings.Join(h.stdin, "\n")
	if !strings.Contains(sql, "CREATE DATABASE `"+NativeDB(s.ID)+"`") || !strings.Contains(sql, "GRANT ALL PRIVILEGES ON `"+NativeDB(s.ID)+"`.*") {
		t.Errorf("database SQL %s", sql)
	}
	if len(calls(f)) != 0 && strings.Contains(calls(f), "compose") {
		t.Errorf("a native website touched Docker:\n%s", calls(f))
	}
	for _, path := range []string{p.fpmConf(s.ID), p.unit(s.ID), p.nginx(s.ID), p.edge(s.ID)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s not written", path)
		}
	}
	unit := mustRead(t, p.unit(s.ID))
	if !strings.Contains(unit, "User="+user) || !strings.Contains(unit, "ExecStart=/usr/sbin/php-fpm8.4 ") || !strings.Contains(unit, "IPAddressDeny=localhost") {
		t.Errorf("unit %s", unit)
	}
	edge := mustRead(t, p.edge(s.ID))
	if !strings.Contains(edge, "http://172.18.0.1:8088") || !strings.Contains(edge, "Host(`voltix.id`)") {
		t.Errorf("edge %s", edge)
	}
	if st, _, _ := m.StatusCount(context.Background(), s.ID); st != "running" {
		t.Errorf("status %s", st)
	}
	if err := m.Action(context.Background(), s.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.nginx(s.ID)); err == nil || m.Status(context.Background(), s.ID) != "stopped" {
		t.Error("stop left the website served")
	}
	if err := m.Action(context.Background(), s.ID, "start"); err != nil || m.Status(context.Background(), s.ID) != "running" {
		t.Errorf("start %v", err)
	}
	if err := m.Action(context.Background(), s.ID, "rm"); !errors.Is(err, ErrBadInput) {
		t.Errorf("unknown action %v", err)
	}
}

// v is the admin password the create job returned.
func v(t *testing.T, m *Manager, job string) string {
	t.Helper()
	return m.Jobs().Get(job).TakeSecret()
}

func TestNativeStaticAndPHP(t *testing.T) {
	m, _, h, p := nativeTest(t)
	job, s, err := m.Create(context.Background(), nativeReq(spec.AppStatic))
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create %+v", v)
	}
	if strings.Contains(h.all(), "systemctl enable") || strings.Contains(h.all(), "mariadb") {
		t.Errorf("a static website got PHP or a database:\n%s", h.all())
	}
	if _, err := os.Stat(p.unit(s.ID)); err == nil {
		t.Error("static website has a PHP service")
	}
	if st := m.Status(context.Background(), s.ID); st != "running" {
		t.Errorf("static status %s", st)
	}
	if _, err := os.Stat(filepath.Join(m.dir(s.ID), "wp", "index.html")); err != nil {
		t.Errorf("no starter page: %v", err)
	}
}

func TestNativeRefused(t *testing.T) {
	m, _ := newTest(t, map[string][]string{"voltix.id": {ip}})
	if _, err := m.Plan(context.Background(), nativeReq("")); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "NATIVE-01") {
		t.Errorf("without the host package: %v", err)
	}
	m, _, _, _ = nativeTest(t)
	c := nativeReq("")
	c.PHP = "8.1"
	if _, err := m.Plan(context.Background(), c); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("PHP not installed: %v", err)
	}
	for name, c := range map[string]spec.Create{
		"apache":  func() spec.Create { c := nativeReq(""); c.Server = "apache"; return c }(),
		"laravel": func() spec.Create { c := nativeReq(spec.AppLaravel); return c }(),
		"engine":  func() spec.Create { c := req(); c.Engine = "lxc"; return c }(),
	} {
		if _, err := m.Plan(context.Background(), c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNativeCreateFailureIsShown(t *testing.T) {
	m, _, h, _ := nativeTest(t)
	h.fail = "nginx -t"
	job, s, err := m.Create(context.Background(), nativeReq(spec.AppStatic))
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "nginx configuration refused") {
		t.Errorf("job %+v", v)
	}
	if got, _ := m.Get(s.ID); got.Phase != "failed" {
		t.Errorf("phase %s", got.Phase)
	}
	if strings.Contains(h.all(), "systemctl reload nginx") {
		t.Error("a refused configuration was reloaded")
	}
}

func TestLoadNative(t *testing.T) {
	dir := t.TempDir()
	if c, err := LoadNative(filepath.Join(dir, "none.json")); c != nil || err != nil {
		t.Errorf("missing file: %v %v", c, err)
	}
	good := `{"gateway":"172.18.0.1","port":8088,"php":["8.4"],"mariadb_socket":"/run/mysqld/mysqld.sock","wp_cli":"/usr/local/bin/wp"}`
	for body, ok := range map[string]bool{
		good: true,
		strings.Replace(good, `"8.4"`, `"7.4"`, 1):             false,
		strings.Replace(good, "8088", "80", 1):                 false,
		strings.Replace(good, "172.18.0.1", "nope", 1):         false,
		strings.Replace(good, `"port"`, `"extra":1,"port"`, 1): false,
	} {
		p := filepath.Join(dir, "n.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := LoadNative(p)
		if (err == nil && c != nil) != ok {
			t.Errorf("%s: %v %v", body, c, err)
		}
	}
	if NativeDB("a-very-long-website-id-that-goes-on-and-on") == NativeDB("a-very-long-website-id-that-goes-on-and-on-2") || len(NativeDB("a-very-long-website-id-that-goes-on-and-on")) > 32 {
		t.Error("NativeDB not unique or too long")
	}
	if wpVersion("8.1") != "6.9.0" || wpVersion("8.4") != "7.1.4" {
		t.Errorf("wp %s %s", wpVersion("8.1"), wpVersion("8.4"))
	}
}
