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

func nativeReady(t *testing.T) (*Manager, *fakeRunner, *fakeHost, NativePaths, Site) {
	t.Helper()
	m, f, h, p := nativeTest(t)
	job, s, err := m.Create(context.Background(), nativeReq(""))
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create %+v", v)
	}
	s, _ = m.Get(s.ID)
	return m, f, h, p, s
}

func TestNativeLogs(t *testing.T) {
	m, _, h, _, s := nativeReady(t)
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "GET /line-"+string(rune('a'+i%26)))
	}
	if err := os.WriteFile(filepath.Join(m.dir(s.ID), "logs", "access.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := m.Logs(context.Background(), s.ID, "web", 5)
	if err != nil || !strings.Contains(out, "==> access.log <==") || strings.Count(out, "GET /line-") != 5 {
		t.Errorf("web logs %v\n%s", err, out)
	}
	if _, err := m.Logs(context.Background(), s.ID, "php", 50); err != nil || !strings.Contains(h.all(), "journalctl --no-pager --output=short-iso --lines 50 --unit "+NativeUnit(s.ID)) {
		t.Errorf("php logs %v", err)
	}
	if _, err := m.Logs(context.Background(), s.ID, "db", 10); err == nil {
		t.Error("the shared database is not a service of the website")
	}
}

func TestNativeTrashRestoreRemove(t *testing.T) {
	m, _, h, p, s := nativeReady(t)
	entry, err := m.Delete(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p.unit(s.ID), p.fpmConf(s.ID), p.nginx(s.ID), p.edge(s.ID)} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s left after delete", f)
		}
	}
	if !strings.Contains(h.all(), "systemctl disable --now "+NativeUnit(s.ID)) || strings.Contains(h.all(), "DROP DATABASE") || strings.Contains(h.all(), "userdel") {
		t.Errorf("trash must stop the website and keep its database and user:\n%s", h.all())
	}
	if _, err := m.Restore(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.edge(s.ID)); err != nil || m.Status(context.Background(), s.ID) != "running" {
		t.Errorf("restore did not bring the website back: %v", err)
	}
	entry, _ = m.Delete(context.Background(), s.ID)
	m.native = nil
	if err := m.RemoveTrash(entry); err == nil {
		t.Error("removed a native website without the native engine (its database would stay behind)")
	}
	m.native = testNative
	if err := m.RemoveTrash(entry); err != nil {
		t.Fatal(err)
	}
	sql := strings.Join(h.stdin, "\n")
	if !strings.Contains(sql, "DROP DATABASE IF EXISTS `"+NativeDB(s.ID)+"`") || !strings.Contains(sql, "DROP USER IF EXISTS") || !strings.Contains(h.all(), "userdel "+NativeUser(s.ID)) {
		t.Errorf("removal left the database or the user:\n%s\n%s", sql, h.all())
	}
}

func TestNativeNeverReachesDocker(t *testing.T) {
	m, f, h, _, s := nativeReady(t)
	before := len(f.calls)
	if st, err := m.Stack(context.Background(), s.ID); err != nil || len(st.Containers) != 2 || st.Containers[0].State != "running" || st.Containers[1].Name != NativeUnit(s.ID) {
		t.Errorf("stack %+v %v", st, err)
	}
	if err := m.RestartService(context.Background(), s.ID, "php"); err != nil || !strings.Contains(h.all(), "systemctl restart "+NativeUnit(s.ID)) {
		t.Errorf("restart php %v", err)
	}
	if err := m.RestartService(context.Background(), s.ID, "web"); err != nil || strings.Contains(h.all(), "systemctl restart nginx") {
		t.Errorf("restart web must reload the shared Nginx: %v", err)
	}
	if len(f.calls) != before {
		t.Errorf("docker called for a native website: %v", f.calls[before:])
	}
}

func TestNativeLimits(t *testing.T) {
	m, f, h, p, s := nativeReady(t)
	l, err := m.Limits(s.ID)
	if err != nil || len(l.Services) != 1 || l.Services[0].Service != "php" || l.Services[0].Standard != NativeMemory || l.PHP == nil || *l.PHP != PHPStandard {
		t.Fatalf("limits %+v %v", l, err)
	}
	if !strings.Contains(mustRead(t, p.unit(s.ID)), "MemoryMax=512M") || !strings.Contains(mustRead(t, p.fpmConf(s.ID)), "memory_limit] = 256M") {
		t.Error("standard limits not written")
	}
	php := PHPStandard
	php.Memory, php.Upload = 512, 256
	if err := m.SetLimits(context.Background(), s.ID, LimitChange{PHP: &php}); err != nil {
		t.Fatal(err)
	}
	fpm, unit, ngx := mustRead(t, p.fpmConf(s.ID)), mustRead(t, p.unit(s.ID)), mustRead(t, p.nginx(s.ID))
	if !strings.Contains(fpm, "memory_limit] = 512M") || !strings.Contains(fpm, "upload_max_filesize] = 256M") || !strings.Contains(unit, "MemoryMax=768M") || !strings.Contains(ngx, "client_max_body_size 256m;") {
		t.Errorf("raised limits not written:\n%s\n%s\n%s", fpm, unit, ngx)
	}
	if !strings.Contains(h.all(), "systemctl restart "+NativeUnit(s.ID)) || strings.Contains(calls(f), "compose") {
		t.Errorf("limits did not restart PHP, or reached Docker")
	}
	h.fail = "systemctl restart"
	php.Memory = 1024
	if err := m.SetLimits(context.Background(), s.ID, LimitChange{PHP: &php}); err == nil || !strings.Contains(err.Error(), "previous") {
		t.Errorf("failure %v", err)
	}
	h.fail = ""
	if got, _ := m.Get(s.ID); got.PHPLimits == nil || got.PHPLimits.Memory != 512 || !strings.Contains(mustRead(t, p.fpmConf(s.ID)), "memory_limit] = 512M") {
		t.Errorf("previous limits not put back: %+v", got.PHPLimits)
	}
}
