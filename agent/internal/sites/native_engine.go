package sites

// The native engine at work (NATIVE-01): creating, running and removing a website on the host's Nginx, its own
// PHP-FPM service and the host's MariaDB. The host package NATIVE-01 installs those and writes native.json; without
// that file the agent offers Docker only.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// NativeConfig is /etc/nostek-agent/native.json, written by the NATIVE-01 host package.
type NativeConfig struct {
	Gateway       string   `json:"gateway"` // the host's address on the edge network: Nginx listens there
	Port          int      `json:"port"`
	PHP           []string `json:"php"`            // PHP-FPM versions installed
	MariaDBSocket string   `json:"mariadb_socket"` // /run/mysqld/mysqld.sock
	WPCLI         string   `json:"wp_cli"`         // /usr/local/bin/wp
}

// LoadNative reads native.json; a missing file means the native engine is not installed (nil, nil).
func LoadNative(path string) (*NativeConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c NativeConfig
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("native.json: %w", err)
	}
	if ip := net.ParseIP(c.Gateway); ip == nil || ip.To4() == nil || c.Port != NativePort {
		return nil, fmt.Errorf("native.json: gateway must be an IPv4 address and port %d", NativePort)
	}
	if !filepath.IsAbs(c.MariaDBSocket) || !filepath.IsAbs(c.WPCLI) || len(c.PHP) == 0 {
		return nil, errors.New("native.json: mariadb_socket and wp_cli must be absolute paths, php must list versions")
	}
	for _, v := range c.PHP {
		if _, ok := catalog.PHP(v); !ok {
			return nil, fmt.Errorf("native.json: PHP %q is not in the catalogue", v)
		}
	}
	return &c, nil
}

// HostRunner runs one of a few fixed host programs (never a shell) for the native engine.
type HostRunner interface {
	Host(ctx context.Context, stdin io.Reader, bin string, args ...string) ([]byte, error)
}

// hostBins are the only programs the native engine runs on the host.
var hostBins = map[string]string{
	"systemctl":    "/usr/bin/systemctl",
	"systemd-run":  "/usr/bin/systemd-run",
	"useradd":      "/usr/sbin/useradd",
	"userdel":      "/usr/sbin/userdel",
	"usermod":      "/usr/sbin/usermod",
	"gpasswd":      "/usr/bin/gpasswd",
	"nginx":        "/usr/sbin/nginx",
	"mariadb":      "/usr/bin/mariadb",
	"mariadb-dump": "/usr/bin/mariadb-dump",
	"ufw":          "/usr/sbin/ufw", // DB-02: the database ports (also without the native engine)
	"git":          "/usr/bin/git",  // UPD-01: which commit a release tag names
	// SRV-01: the SSH ports (never closed from the panel) and fail2ban
	"sshd":            "/usr/sbin/sshd",
	"fail2ban-client": "/usr/bin/fail2ban-client",
	// BAK-03: copies to an SFTP server
	"sftp":        "/usr/bin/sftp",
	"ssh-keygen":  "/usr/bin/ssh-keygen",
	"ssh-keyscan": "/usr/bin/ssh-keyscan",
}

// HostStreamer is a HostRunner that can send a program's output to a writer (database dumps of any size).
type HostStreamer interface {
	HostTo(ctx context.Context, stdin io.Reader, w io.Writer, bin string, args ...string) ([]byte, error)
}

// ExecHost runs the real programs.
type ExecHost struct{}

// HostTo runs bin with stdout going to w; it returns stderr.
func (ExecHost) HostTo(ctx context.Context, stdin io.Reader, w io.Writer, bin string, args ...string) ([]byte, error) {
	path, ok := hostBins[bin]
	if !ok {
		return nil, fmt.Errorf("host program %q is not allowed", bin)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &errb
	err := cmd.Run()
	return errb.Bytes(), err
}

// Host runs bin with args and returns stdout+stderr.
func (ExecHost) Host(ctx context.Context, stdin io.Reader, bin string, args ...string) ([]byte, error) {
	path, ok := hostBins[bin]
	if !ok {
		return nil, fmt.Errorf("host program %q is not allowed", bin)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = stdin
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// EnableNative turns the native engine on (main, when native.json exists; tests with a fake host).
func (m *Manager) EnableNative(c *NativeConfig, h HostRunner, p NativePaths) {
	m.native, m.host, m.npaths = c, h, p
	if m.lookupUser == nil {
		m.lookupUser = lookupUser
	}
}

// NativePHP lists the PHP versions of the native engine (nil = the engine is not installed).
func (m *Manager) NativePHP() []string {
	if m.native == nil {
		return nil
	}
	return append([]string(nil), m.native.PHP...)
}

func lookupUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

// errNativeOff answers a change to a native website on a server where the native engine is not (or no longer) on.
var errNativeOff = errors.New("the native engine is off on this server (NATIVE-01 not applied): the website cannot be changed")

// ErrNativeUnsupported answers a Docker-only feature asked of a native website.
var ErrNativeUnsupported = fmt.Errorf("%w: this is not available for a native website", ErrBadInput)

// dockerOnly refuses a Docker command for a native website: every Docker path goes through it, so a native
// website can never reach a compose call made for a stack it does not have.
func (m *Manager) dockerOnly(id string) error {
	if s, err := readSite(m.dir(id)); err == nil && s.IsNative() {
		return ErrNativeUnsupported
	}
	return nil
}

// checkNative refuses a native website the server cannot run.
func (m *Manager) checkNative(c spec.Create) error {
	if c.Engine != spec.EngineNative {
		return nil
	}
	if m.native == nil {
		return fmt.Errorf("%w: the native engine is not installed on this server (run NATIVE-01 first)", ErrBadInput)
	}
	if c.AppOf() != spec.AppStatic && !slices.Contains(m.native.PHP, c.PHP) {
		return fmt.Errorf("%w: PHP %s is not installed for the native engine", ErrBadInput, c.PHP)
	}
	return nil
}

// NativeDB is the database name and user of a native website (MariaDB allows 64 characters; kept to 32).
func NativeDB(id string) string {
	n := "np_" + strings.ReplaceAll(id, "-", "_")
	if len(n) <= 32 {
		return n
	}
	h := sha256.Sum256([]byte(id))
	return strings.TrimRight(n[:24], "_") + "_" + hex.EncodeToString(h[:])[:6]
}

// wpVersion is the WordPress release the catalogue pins for a PHP version (the native engine downloads the same).
func wpVersion(php string) string {
	p, _ := catalog.PHP(php)
	name, _, _ := strings.Cut(p.Apache, "@")
	v, _, _ := strings.Cut(strings.TrimPrefix(name, "wordpress:"), "-")
	return v
}

func (m *Manager) hostRun(ctx context.Context, stdin io.Reader, bin string, args ...string) ([]byte, error) {
	return m.host.Host(ctx, stdin, bin, args...)
}

// nativeWP runs WP-CLI as the website's user, in the website's sandbox.
func (m *Manager) nativeWP(ctx context.Context, s Site, stdin io.Reader, args ...string) ([]byte, error) {
	return m.hostRun(ctx, stdin, "systemd-run", m.nativeWPArgs(s, nil, args)...)
}

// nativeWPArgs is the systemd-run command line of one WP-CLI run: the website's user, its sandbox, its PHP.
func (m *Manager) nativeWPArgs(s Site, env map[string]string, args []string) []string {
	u := NativeUser(s.ID)
	// no $VAR expansion: WP-CLI eval code holds PHP variables
	a := []string{"--quiet", "--pipe", "--wait", "--collect", "--expand-environment=no", "--uid=" + u, "--gid=" + u, "--working-directory=" + m.npaths.docroot(s.ID)}
	for _, p := range s.nativeSandbox(m.npaths) {
		a = append(a, "-p", p)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		a = append(a, "--setenv="+k+"="+env[k])
	}
	a = append(a, "--", "/usr/bin/php"+s.PHP, m.native.WPCLI, "--path="+m.npaths.docroot(s.ID))
	return append(a, args...)
}

// nativeWPBatch runs WP-CLI commands one after the other for the WordPress Tools, each with its own output and
// exit code (the Docker way runs them in one shell in the CLI container).
func (m *Manager) nativeWPBatch(ctx context.Context, s Site, env map[string]string, cmds [][]string) ([]wpOut, error) {
	if m.native == nil {
		return nil, errNativeOff
	}
	st, ok := m.host.(HostStreamer)
	if !ok {
		return nil, errors.New("WP-CLI is not available")
	}
	res := make([]wpOut, len(cmds))
	for i, c := range cmds {
		if len(c) == 0 || c[0] != "wp" {
			return nil, fmt.Errorf("not a WP-CLI command: %v", c)
		}
		var out bytes.Buffer
		errOut, err := st.HostTo(ctx, nil, &out, "systemd-run", m.nativeWPArgs(s, env, c[1:])...)
		res[i] = wpOut{Out: out.String(), Err: string(errOut)}
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			res[i].RC = ee.ExitCode()
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, errors.New("WP-CLI took too long and was stopped")
		default:
			res[i].RC = 1
			if res[i].Err == "" {
				res[i].Err = err.Error()
			}
		}
	}
	return res, nil
}

// nativeMyCnf is the website's own database login (0600, in its 0700 secrets folder, readable by root only).
func (m *Manager) nativeMyCnf(id string) string { return filepath.Join(m.dir(id), "secrets", "my.cnf") }

// nativeClient runs the MariaDB client as the website's own database user, on its own database: the Database page
// can reach nothing else on the shared server.
func (m *Manager) nativeClient(ctx context.Context, s Site, stdin io.Reader, args ...string) ([]byte, error) {
	if m.native == nil {
		return nil, errNativeOff
	}
	a := append([]string{"--defaults-extra-file=" + m.nativeMyCnf(s.ID), "--protocol=socket", "--socket=" + m.native.MariaDBSocket}, args...)
	return m.hostRun(ctx, stdin, "mariadb", append(a, NativeDB(s.ID))...)
}

// nativeSQL runs SQL on the host's MariaDB as root over its socket (the statement on stdin, never in argv).
func (m *Manager) nativeSQL(ctx context.Context, sql string) ([]byte, error) {
	return m.hostRun(ctx, strings.NewReader(sql), "mariadb", "--protocol=socket", "--socket="+m.native.MariaDBSocket, "--batch", "--skip-column-names")
}

// chownTree gives a folder and everything in it to the website's user (links are changed, never followed).
func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

// nginxReload checks the whole Nginx configuration before reloading it: a bad file never takes the others down.
func (m *Manager) nginxReload(ctx context.Context) ([]byte, error) {
	if out, err := m.hostRun(ctx, nil, "nginx", "-t", "-q"); err != nil {
		return out, fmt.Errorf("nginx configuration refused: %w", err)
	}
	return m.hostRun(ctx, nil, "systemctl", "reload", "nginx.service")
}

// writeNativeFiles writes the PHP-FPM pool, the service, the Nginx server and the edge entry of a website.
func (m *Manager) writeNativeFiles(s Site) error {
	p := m.npaths
	files := map[string]string{p.nginx(s.ID): s.NativeNginx(p, m.native.Gateway)}
	if s.nativePHP() {
		files[p.fpmConf(s.ID)] = s.NativeFPM(p)
		files[p.unit(s.ID)] = s.NativeService(p)
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return m.writeNativeEdge(s)
}

// writeNativeEdge writes (or, with nothing routed, removes) the website's Traefik file-provider entry.
func (m *Manager) writeNativeEdge(s Site) error {
	path := m.npaths.edge(s.ID)
	body := s.NativeEdge(m.native.Gateway)
	if body == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFile(path, []byte(body), 0o644)
}

// createNative is the create job of a native website.
func (m *Manager) createNative(ctx context.Context, j *Job, s Site, fail func(string, error, []byte)) {
	dir := m.dir(s.ID)
	u := NativeUser(s.ID)
	j.Step("files")
	for _, d := range []string{dir, filepath.Join(dir, "wp"), filepath.Join(dir, "tmp", "sessions"), filepath.Join(dir, "tmp", "wp-cli"), filepath.Join(dir, "logs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil { // the site folder itself: the agent's, as for Docker websites
		fail("files", err, nil)
		return
	}
	if err := writeSite(dir, s); err != nil {
		fail("files", err, nil)
		return
	}
	j.Step("website user")
	if out, err := m.hostRun(ctx, nil, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", filepath.Join(dir, "tmp"), "--shell", "/usr/sbin/nologin", u); err != nil {
		fail("website user", err, out)
		return
	}
	// the host's Nginx reads the website's files through the website's group
	if out, err := m.hostRun(ctx, nil, "usermod", "--append", "--groups", u, "www-data"); err != nil {
		fail("website user", err, out)
		return
	}
	uid, gid, err := m.lookupUser(u)
	if err != nil {
		fail("website user", err, nil)
		return
	}
	if s.AppName() != spec.AppWordPress {
		if err := m.writeStarter(s); err != nil {
			fail("files", err, nil)
			return
		}
	}
	for _, d := range []string{"wp", "tmp", "logs"} {
		if err := chownTree(filepath.Join(dir, d), uid, gid); err != nil {
			fail("files", err, nil)
			return
		}
	}
	pw := Password()
	if s.HasDB() {
		j.Step("database")
		db := NativeDB(s.ID)
		// the website's own database login, for the Database page (never root, never in a command line)
		if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
			fail("database", err, nil)
			return
		}
		if err := writeFile(m.nativeMyCnf(s.ID), []byte("[client]\nuser="+db+"\npassword="+pw+"\n"), 0o600); err != nil {
			fail("database", err, nil)
			return
		}
		sql := fmt.Sprintf("CREATE DATABASE `%[1]s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\nCREATE USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\nGRANT ALL PRIVILEGES ON `%[1]s`.* TO '%[1]s'@'localhost';\n", db, pw)
		if out, err := m.nativeSQL(ctx, sql); err != nil {
			fail("database", err, out)
			return
		}
	}
	j.Step("web server")
	if err := m.writeNativeFiles(s); err != nil {
		fail("web server", err, nil)
		return
	}
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "daemon-reload"); err != nil {
			fail("web server", err, out)
			return
		}
		if out, err := m.hostRun(ctx, nil, "systemctl", "enable", "--now", NativeUnit(s.ID)); err != nil {
			fail("start PHP", err, out)
			return
		}
	}
	if out, err := m.nginxReload(ctx); err != nil {
		fail("web server", err, out)
		return
	}
	if s.IsWP() {
		j.Step("install WordPress")
		db := NativeDB(s.ID)
		steps := []struct {
			stdin string
			args  []string
		}{
			{"", []string{"core", "download", "--version=" + wpVersion(s.PHP)}},
			{pw + "\n", []string{"config", "create", "--dbname=" + db, "--dbuser=" + db, "--dbhost=localhost:" + m.native.MariaDBSocket, "--prompt=dbpass", "--skip-check"}},
		}
		admin := Password()
		steps = append(steps,
			struct {
				stdin string
				args  []string
			}{admin + "\n", []string{"core", "install", "--url=" + s.URL, "--title=" + s.Title, "--admin_user=" + s.AdminUser, "--admin_email=" + s.AdminEmail, "--skip-email", "--prompt=admin_password"}},
			struct {
				stdin string
				args  []string
			}{"", []string{"rewrite", "structure", "/%postname%/"}},
		)
		for _, st := range steps {
			var in io.Reader
			if st.stdin != "" {
				in = strings.NewReader(st.stdin)
			}
			if out, err := m.nativeWP(ctx, s, in, st.args...); err != nil {
				fail("install WordPress", err, out)
				return
			}
		}
		pw = admin
	}
	s.Phase = "ready"
	if err := writeSite(dir, s); err != nil {
		fail("finish", err, nil)
		return
	}
	if s.IsWP() {
		j.Done(pw)
		return
	}
	j.Done("")
}

// nativeStatus is running when the website's PHP-FPM service is active (a static website: its Nginx server is
// enabled), stopped otherwise.
func (m *Manager) nativeStatus(ctx context.Context, s Site) (string, int, int) {
	if !s.nativePHP() {
		if _, err := os.Stat(m.npaths.nginx(s.ID)); err == nil {
			return "running", 1, 1
		}
		return "stopped", 0, 1
	}
	out, _ := m.hostRun(ctx, nil, "systemctl", "is-active", NativeUnit(s.ID))
	if strings.TrimSpace(string(out)) == "active" {
		return "running", 1, 1
	}
	return "stopped", 0, 1
}

// nativeAction starts, stops or restarts a native website. Stopping takes its Nginx server out too, so visitors
// get the edge's "not found" as for a stopped Docker website.
func (m *Manager) nativeAction(ctx context.Context, s Site, action string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conf := m.npaths.nginx(s.ID)
	switch action {
	case "start", "restart":
		if err := writeFile(conf, []byte(s.NativeNginx(m.npaths, m.native.Gateway)), 0o644); err != nil {
			return err
		}
		if s.nativePHP() {
			verb := "start"
			if action == "restart" {
				verb = "restart"
			}
			if out, err := m.hostRun(ctx, nil, "systemctl", verb, NativeUnit(s.ID)); err != nil {
				return fmt.Errorf("%s failed: %s", action, tail(out))
			}
		}
	case "stop":
		if s.nativePHP() {
			if out, err := m.hostRun(ctx, nil, "systemctl", "stop", NativeUnit(s.ID)); err != nil {
				return fmt.Errorf("stop failed: %s", tail(out))
			}
		}
		if err := os.Remove(conf); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown action %q", ErrBadInput, action)
	}
	if out, err := m.nginxReload(ctx); err != nil {
		return fmt.Errorf("%s failed: %s", action, tail(out))
	}
	return nil
}
