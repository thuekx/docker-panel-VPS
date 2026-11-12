// Package sites creates and runs WordPress site stacks from the agent's fixed templates.
package sites

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// TrashDays is how long a deleted site stays restorable (SITE-DELETE-1).
const TrashDays = 7

// Runner runs the docker CLI. Tests replace it.
type Runner interface {
	Docker(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error)
}

// ExecRunner runs the real docker binary with an argument list (never a shell).
type ExecRunner struct{ Bin string }

// Docker runs docker in dir and returns stdout+stderr.
func (r ExecRunner) Docker(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "docker"
	}
	cmd := r.cmd(ctx, bin, dir, args)
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

func (r ExecRunner) cmd(ctx context.Context, bin, dir string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+v)
	}
	return cmd
}

// Resolver looks up host addresses. Tests replace it.
type Resolver func(ctx context.Context, host string) ([]string, error)

// Config is the agent's fixed configuration.
type Config struct {
	Data        string // /srv/nostek
	PublicIPv4  string // the server's public address; a host is routed only when it resolves here
	PanelDomain string // never usable as a site host
	PreviewZone string // e.g. preview.nostek.dev ("" = no preview addresses)
	FileUID     int    // owner of files written by the file manager (0 = www-data, 33)
}

// Manager owns all site directories. One change runs at a time.
type Manager struct {
	cfg     Config
	run     Runner
	resolve Resolver
	now     func() time.Time
	mu      sync.Mutex // serialises changes
	jobs    *Jobs
	// tlsAddr and tlsRoots let tests point the certificate check at their own server (default: the local edge).
	tlsAddr  string
	tlsRoots *x509.CertPool
}

// New returns a manager.
func New(cfg Config, run Runner, resolve Resolver) *Manager {
	if resolve == nil {
		resolve = net.DefaultResolver.LookupHost
	}
	return &Manager{cfg: cfg, run: run, resolve: resolve, now: time.Now, jobs: NewJobs()}
}

// Jobs exposes the job table.
func (m *Manager) Jobs() *Jobs { return m.jobs }

func (m *Manager) sitesDir() string { return filepath.Join(m.cfg.Data, "sites") }
func (m *Manager) trashDir() string { return filepath.Join(m.cfg.Data, "trash") }
func (m *Manager) dir(id string) string {
	return filepath.Join(m.sitesDir(), id)
}

// ErrNotFound is returned for an unknown site id.
var ErrNotFound = errors.New("site not found")

// Busy is returned while another change runs.
var ErrBusy = errors.New("another change is running; try again when it is done")

// ErrBadInput marks a request the caller can fix (answered as 400).
var ErrBadInput = errors.New("bad input")

func readSite(dir string) (Site, error) {
	var s Site
	b, err := os.ReadFile(filepath.Join(dir, "site.json"))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeSite(dir string, s Site) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return writeFile(filepath.Join(dir, "site.json"), append(b, '\n'), 0o644)
}

// Get returns one site.
func (m *Manager) Get(id string) (Site, error) {
	if spec.ID(id) != nil {
		return Site{}, ErrNotFound
	}
	s, err := readSite(m.dir(id))
	if errors.Is(err, os.ErrNotExist) {
		return s, ErrNotFound
	}
	return s, err
}

// List returns all sites, sorted by id.
func (m *Manager) List() ([]Site, error) {
	ents, err := os.ReadDir(m.sitesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Site
	for _, e := range ents {
		if !e.IsDir() || spec.ID(e.Name()) != nil {
			continue
		}
		if s, err := readSite(filepath.Join(m.sitesDir(), e.Name())); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Trashed is a site in the trash.
type Trashed struct {
	Site
	Entry   string `json:"entry"`   // trash folder name
	Deleted int64  `json:"deleted"` // unix seconds
	Purge   int64  `json:"purge"`   // unix seconds when it is removed for good
}

var trashRe = regexp.MustCompile(`^([a-z0-9-]{1,40})\.([0-9]{9,12})$`)

// Trash lists the trash.
func (m *Manager) Trash() ([]Trashed, error) {
	ents, err := os.ReadDir(m.trashDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Trashed
	for _, e := range ents {
		mm := trashRe.FindStringSubmatch(e.Name())
		if !e.IsDir() || mm == nil {
			continue
		}
		s, err := readSite(filepath.Join(m.trashDir(), e.Name()))
		if err != nil {
			continue
		}
		var at int64
		fmt.Sscan(mm[2], &at)
		out = append(out, Trashed{Site: s, Entry: e.Name(), Deleted: at, Purge: at + TrashDays*86400})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Entry < out[j].Entry })
	return out, nil
}

// reserved returns every host name in use (panel, live sites, trash).
func (m *Manager) reserved() (map[string]bool, error) {
	r := map[string]bool{}
	if m.cfg.PanelDomain != "" {
		r[m.cfg.PanelDomain] = true
	}
	live, err := m.List()
	if err != nil {
		return nil, err
	}
	tr, err := m.Trash()
	if err != nil {
		return nil, err
	}
	for _, s := range live {
		for _, h := range s.Hosts {
			r[h.Name] = true
		}
	}
	for _, t := range tr {
		for _, h := range t.Hosts {
			r[h.Name] = true
		}
	}
	return r, nil
}

// newID derives a readable id from the domain ("voltix.id" -> "voltix-id"), adding a suffix when taken.
func (m *Manager) newID(domain string) string {
	base := strings.ReplaceAll(domain, ".", "-")
	if len(base) > 32 {
		base = strings.Trim(base[:32], "-")
	}
	taken := func(id string) bool {
		if _, err := os.Stat(m.dir(id)); err == nil {
			return true
		}
		ents, _ := os.ReadDir(m.trashDir())
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), id+".") {
				return true
			}
		}
		return false
	}
	id := base
	for i := 2; taken(id) || spec.ID(id) != nil; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func (m *Manager) pointsHere(ctx context.Context, host string) bool {
	if m.cfg.PublicIPv4 == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := m.resolve(ctx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || ip.To4() == nil {
			continue // IPv6 is not served yet; an AAAA elsewhere does not block
		}
		if a != m.cfg.PublicIPv4 {
			return false
		}
	}
	for _, a := range addrs {
		if a == m.cfg.PublicIPv4 {
			return true
		}
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Password returns a random password of 24 characters from an unambiguous alphabet.
func Password() string {
	const alpha = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}

// Plan checks a create request and returns the site it would make, without changing anything.
func (m *Manager) Plan(ctx context.Context, c spec.Create) (Site, error) {
	res, err := m.reserved()
	if err != nil {
		return Site{}, err
	}
	if err := c.Check(res); err != nil {
		return Site{}, err
	}
	id := m.newID(c.Domain)
	s := Site{
		ID: id, Domain: c.Domain, Primary: c.Primary, Server: c.Server, PHP: c.PHP,
		AdminEmail: c.AdminEmail, Title: c.Title, Created: m.now().Unix(), Phase: "creating",
		Database: c.Database,
		Version:  laravelVersion(c),
	}
	if c.AppOf() == spec.AppNode {
		s.Version, s.Repo, s.Branch, s.Build, s.Start = c.Version, c.Repo, c.Branch, c.Build, c.Start
	}
	if c.AppOf() != spec.AppWordPress {
		s.App = c.AppOf()
	}
	for i, h := range c.Hosts() {
		role := "alias"
		switch i {
		case 0:
			role = "primary"
		case 1:
			role = "www"
		}
		s.Hosts = append(s.Hosts, Host{Name: h, Role: role, Routed: m.pointsHere(ctx, h)})
	}
	if m.cfg.PreviewZone != "" {
		p := id + "." + m.cfg.PreviewZone
		if spec.Domain(p) == nil && !res[p] {
			s.Hosts = append(s.Hosts, Host{Name: p, Role: "preview", Routed: m.pointsHere(ctx, p)})
		}
	}
	if s.Canonical() == "" {
		return Site{}, errors.New("no address of this site points to this server yet (DNS); add the A record or use a preview address")
	}
	s.URL = "https://" + s.Canonical()
	if s.IsWP() {
		s.AdminUser = "admin-" + randHex(3)
	}
	return s, nil
}

// Create starts a create job and returns its id.
func (m *Manager) Create(ctx context.Context, c spec.Create) (string, Site, error) {
	if !m.mu.TryLock() {
		return "", Site{}, ErrBusy
	}
	s, err := m.Plan(ctx, c)
	if err != nil {
		m.mu.Unlock()
		return "", Site{}, err
	}
	j := m.jobs.New("create", s.ID)
	go func() {
		defer m.mu.Unlock()
		m.create(j, s)
	}()
	return j.ID, s, nil
}

func (m *Manager) compose(ctx context.Context, id string, stdin io.Reader, args ...string) ([]byte, error) {
	dir := m.dir(id)
	full := append([]string{"compose", "--project-directory", dir, "-p", Project(id), "-f", filepath.Join(dir, "compose.yml")}, args...)
	return m.run.Docker(ctx, dir, stdin, full...)
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 600 {
		s = "..." + s[len(s)-600:]
	}
	return s
}

func (m *Manager) create(j *Job, s Site) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dir := m.dir(s.ID)
	fail := func(step string, err error, out []byte) {
		j.Fail(fmt.Sprintf("%s: %v %s", step, err, tail(out)))
		s.Phase = "failed"
		_ = writeSite(dir, s)
	}
	if s.IsNode() {
		m.createNode(ctx, j, s, fail)
		return
	}
	j.Step("files")
	dirs := []string{dir, filepath.Join(dir, "wp")}
	if s.HasDB() {
		dirs = append(dirs, filepath.Join(dir, "db"))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		fail("files", err, nil)
		return
	}
	// The secret file is bind-mounted into the site's own containers only (mariadb runs as uid 999, so it must
	// be world-readable inside); the folder above it is 0700, so no other host user can read it.
	if err := writeFile(filepath.Join(dir, "secrets", "db_password"), []byte(Password()), 0o444); err != nil {
		fail("files", err, nil)
		return
	}
	cf, err := s.Compose()
	if err != nil {
		fail("files", err, nil)
		return
	}
	p, _ := catalog.PHP(s.PHP)
	files := map[string]string{"compose.yml": cf}
	switch {
	case s.IsLaravel():
		files["nginx.conf"] = NginxLaravelConf
		files["laravel.ini"] = LaravelIni
		files["laravel-env.php"] = LaravelPrepend
		files[filepath.Join("build", "Dockerfile")] = catalog.LaravelDockerfile(p)
	case s.AppName() == spec.AppStatic:
		files["nginx.conf"] = NginxStaticConf
	case s.IsWP() && s.Server == "nginx":
		files["nginx.conf"] = NginxConf
	case s.Server == "nginx":
		files["nginx.conf"] = NginxPHPConf
	}
	if s.AppName() != spec.AppStatic {
		files["php.ini"] = PHPIni
	}
	if s.IsLaravel() {
		// composer runs as www-data and fills the empty website folder; the build context holds only the recipe
		if err := os.MkdirAll(filepath.Join(dir, "build"), 0o755); err != nil {
			fail("files", err, nil)
			return
		}
		if err := os.Chown(filepath.Join(dir, "wp"), m.uid(), m.uid()); err != nil {
			fail("files", err, nil)
			return
		}
	}
	for name, body := range files {
		if err := writeFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if !s.IsWP() && !s.IsLaravel() {
		if err := m.writeStarter(s); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := writeSite(dir, s); err != nil {
		fail("files", err, nil)
		return
	}
	if s.IsLaravel() {
		j.Step("build PHP image")
		if out, err := m.compose(ctx, s.ID, nil, "build", "--quiet", "php"); err != nil {
			fail("build PHP image", err, out)
			return
		}
	}
	j.Step("pull images")
	if out, err := m.compose(ctx, s.ID, nil, "--profile", "tools", "pull", "--quiet", "--policy", "missing", "--ignore-buildable"); err != nil {
		fail("pull images", err, out)
		return
	}
	if s.IsLaravel() {
		j.Step("install Laravel")
		if out, err := m.installLaravel(ctx, s); err != nil {
			fail("install Laravel", err, out)
			return
		}
	}
	j.Step("start containers")
	if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		fail("start containers", err, out)
		return
	}
	if !s.IsWP() {
		s.Phase = "ready"
		if err := writeSite(dir, s); err != nil {
			fail("finish", err, nil)
			return
		}
		j.Done("")
		return
	}
	j.Step("install WordPress")
	pw := Password()
	out, err := m.compose(ctx, s.ID, strings.NewReader(pw+"\n"), "run", "--rm", "-T", "cli",
		"wp", "core", "install", "--url="+s.URL, "--title="+s.Title, "--admin_user="+s.AdminUser,
		"--admin_email="+s.AdminEmail, "--skip-email", "--prompt=admin_password")
	if err != nil {
		fail("install WordPress", err, out)
		return
	}
	if out, err := m.compose(ctx, s.ID, nil, "run", "--rm", "-T", "cli", "wp", "rewrite", "structure", "/%postname%/"); err != nil {
		fail("install WordPress", err, out)
		return
	}
	s.Phase = "ready"
	if err := writeSite(dir, s); err != nil {
		fail("finish", err, nil)
		return
	}
	j.Done(pw)
}

// Status reads the state of each container of a site: running | stopped | partial | unknown.
func (m *Manager) Status(ctx context.Context, id string) string {
	st, _, _ := m.StatusCount(ctx, id)
	return st
}

// StatusCount is Status plus how many containers are up out of how many (STACK-01).
func (m *Manager) StatusCount(ctx context.Context, id string) (string, int, int) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := m.compose(ctx, id, nil, "ps", "--all", "--format", "json")
	if err != nil {
		return "unknown", 0, 0
	}
	rows, err := parsePS(out)
	if err != nil {
		return "unknown", 0, 0
	}
	if len(rows) == 0 {
		return "stopped", 0, 0
	}
	up := 0
	for _, r := range rows {
		if r.State == "running" && r.Health != "unhealthy" && r.Health != "starting" {
			up++
		}
	}
	switch {
	case up == len(rows):
		return "running", up, len(rows)
	case up == 0:
		return "stopped", up, len(rows)
	default:
		return "partial", up, len(rows)
	}
}

// Action runs start, stop or restart.
func (m *Manager) Action(ctx context.Context, id, action string) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var args []string
	switch action {
	case "start":
		args = []string{"up", "-d", "--wait", "--wait-timeout", "180"}
	case "stop":
		args = []string{"stop"}
	case "restart":
		args = []string{"restart"}
	default:
		return fmt.Errorf("action %q is not allowed", action)
	}
	if out, err := m.compose(ctx, id, nil, args...); err != nil {
		return fmt.Errorf("%s failed: %s", action, tail(out))
	}
	return nil
}

// Logs returns the last lines of one service.
func (m *Manager) Logs(ctx context.Context, id, service string, lines int) (string, error) {
	s, err := m.Get(id)
	if err != nil {
		return "", err
	}
	ok := service == "web" || service == "db" || (service == "php" && s.Server == "nginx")
	if !ok {
		return "", fmt.Errorf("service %q is not part of this site", service)
	}
	if lines < 1 || lines > 1000 {
		return "", errors.New("lines must be 1..1000")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := m.compose(ctx, id, nil, "logs", "--no-color", "--timestamps", "--tail", fmt.Sprint(lines), service)
	if err != nil {
		return "", fmt.Errorf("logs failed: %s", tail(out))
	}
	return string(out), nil
}

// Delete stops the site and moves its folder to the trash (files and database are kept for TrashDays).
func (m *Manager) Delete(ctx context.Context, id string) (string, error) {
	if _, err := m.Get(id); err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if out, err := m.compose(ctx, id, nil, "--profile", "tools", "down", "--remove-orphans"); err != nil {
		return "", fmt.Errorf("stop failed: %s", tail(out))
	}
	if err := os.MkdirAll(m.trashDir(), 0o700); err != nil {
		return "", err
	}
	entry := fmt.Sprintf("%s.%d", id, m.now().Unix())
	if err := os.Rename(m.dir(id), filepath.Join(m.trashDir(), entry)); err != nil {
		return "", err
	}
	return entry, nil
}

// Restore moves a trash entry back and starts it.
func (m *Manager) Restore(ctx context.Context, entry string) (string, error) {
	mm := trashRe.FindStringSubmatch(entry)
	if mm == nil || spec.ID(mm[1]) != nil {
		return "", ErrNotFound
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	src := filepath.Join(m.trashDir(), entry)
	if _, err := os.Stat(filepath.Join(src, "site.json")); err != nil {
		return "", ErrNotFound
	}
	id := mm[1]
	if _, err := os.Stat(m.dir(id)); err == nil {
		return "", fmt.Errorf("a site %s exists again; delete it first", id)
	}
	if err := os.MkdirAll(m.sitesDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, m.dir(id)); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if out, err := m.compose(ctx, id, nil, "up", "-d", "--wait", "--wait-timeout", "180"); err != nil {
		return id, fmt.Errorf("restored, but start failed: %s", tail(out))
	}
	return id, nil
}

// Purge removes trash entries older than TrashDays and returns their names.
func (m *Manager) Purge() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr, err := m.Trash()
	if err != nil {
		return nil, err
	}
	var gone []string
	now := m.now().Unix()
	for _, t := range tr {
		if t.Purge > now {
			continue
		}
		p := filepath.Join(m.trashDir(), t.Entry)
		if filepath.Dir(p) != m.trashDir() || trashRe.FindString(filepath.Base(p)) == "" {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return gone, err
		}
		gone = append(gone, t.Entry)
	}
	gone = append(gone, m.purgeFiles()...)
	return gone, nil
}

// Container is one container of a site's stack, as the panel shows it (STACK-01).
type Container struct {
	Service  string  `json:"service"`   // web | php | db
	Name     string  `json:"name"`      // docker container name
	Image    string  `json:"image"`     // image reference without the digest
	State    string  `json:"state"`     // running | exited | restarting | created | paused | dead
	Health   string  `json:"health"`    // healthy | unhealthy | starting | "" (no health check)
	Status   string  `json:"status"`    // docker's own short text, e.g. "Up 2 hours (healthy)"
	CPU      float64 `json:"cpu"`       // percent of one CPU, -1 when unknown
	MemBytes int64   `json:"mem"`       // bytes in use, -1 when unknown
	MemLimit int64   `json:"mem_limit"` // bytes, 0 when unknown
	Restarts int     `json:"restarts"`
	Started  int64   `json:"started"` // unix seconds, 0 when not running
}

// Stack is a site's compose project and its containers.
type Stack struct {
	Project    string      `json:"project"`
	Containers []Container `json:"containers"`
}

// services lists the services a site may have; anything else in the project is ignored.
func services(s Site) []string {
	if s.IsNode() {
		return []string{"app"}
	}
	l := []string{"web"}
	if s.Server == "nginx" && s.AppName() != spec.AppStatic {
		l = append(l, "php")
	}
	if s.HasDB() {
		l = append(l, "db")
	}
	if s.IsLaravel() {
		l = append(l, "queue", "scheduler")
	}
	return l
}

func validService(s Site, svc string) bool {
	for _, x := range services(s) {
		if x == svc {
			return true
		}
	}
	return false
}

type psRow struct {
	ID      string `json:"ID"`
	Name    string `json:"Name"`
	Service string `json:"Service"`
	Image   string `json:"Image"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Status  string `json:"Status"`
}

// parsePS reads `docker compose ps --format json` (one object per line, or one array on older versions).
func parsePS(out []byte) ([]psRow, error) {
	var rows []psRow
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "["):
			var many []psRow
			if err := json.Unmarshal([]byte(line), &many); err != nil {
				return nil, err
			}
			rows = append(rows, many...)
		default:
			var r psRow
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return nil, err
			}
			rows = append(rows, r)
		}
	}
	return rows, nil
}

var sizeRe = regexp.MustCompile(`^([0-9.]+)\s*([KMGT]?i?B)$`)

// parseSize reads docker's sizes like "45.2MiB" or "1.5GB" (B, kB, MB, GB, KiB, MiB, GiB, TiB).
func parseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(strings.Replace(s, "kB", "KB", 1)))
	if m == nil {
		return -1
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return -1
	}
	mult := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[m[2]]
	if mult == 0 {
		return -1
	}
	return int64(v * mult)
}

// Stack reads the containers of a site with their live usage. A docker failure gives an error
// (the panel then shows "unknown", never green).
func (m *Manager) Stack(ctx context.Context, id string) (Stack, error) {
	s, err := m.Get(id)
	if err != nil {
		return Stack{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := m.compose(ctx, id, nil, "ps", "--all", "--format", "json")
	if err != nil {
		return Stack{}, fmt.Errorf("cannot read containers: %s", tail(out))
	}
	rows, err := parsePS(out)
	if err != nil {
		return Stack{}, errors.New("cannot read containers: unexpected docker output")
	}
	st := Stack{Project: Project(id), Containers: []Container{}}
	byID := map[string]int{}
	var ids []string
	for _, r := range rows {
		if !validService(s, r.Service) {
			continue
		}
		img := r.Image
		if i := strings.Index(img, "@"); i > 0 {
			img = img[:i]
		}
		c := Container{Service: r.Service, Name: r.Name, Image: img, State: r.State, Health: r.Health, Status: r.Status, CPU: -1, MemBytes: -1}
		byID[r.ID] = len(st.Containers)
		st.Containers = append(st.Containers, c)
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return st, nil
	}
	// restart counts and start times
	if out, err := m.run.Docker(ctx, m.dir(id), nil, append([]string{"inspect", "--format", "json"}, ids...)...); err == nil {
		var ins []struct {
			ID           string `json:"Id"`
			RestartCount int    `json:"RestartCount"`
			State        struct {
				Running   bool   `json:"Running"`
				StartedAt string `json:"StartedAt"`
			} `json:"State"`
		}
		if json.Unmarshal(out, &ins) == nil {
			for _, x := range ins {
				for short, i := range byID {
					if short != "" && strings.HasPrefix(x.ID, short) {
						st.Containers[i].Restarts = x.RestartCount
						if t, err := time.Parse(time.RFC3339Nano, x.State.StartedAt); err == nil && x.State.Running {
							st.Containers[i].Started = t.Unix()
						}
					}
				}
			}
		}
	}
	// live CPU and memory of running containers
	var running []string
	for short, i := range byID {
		if st.Containers[i].State == "running" && short != "" {
			running = append(running, short)
		}
	}
	if len(running) > 0 {
		args := append([]string{"stats", "--no-stream", "--no-trunc", "--format", "json"}, running...)
		if out, err := m.run.Docker(ctx, m.dir(id), nil, args...); err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				var x struct {
					ID       string `json:"ID"`
					CPUPerc  string `json:"CPUPerc"`
					MemUsage string `json:"MemUsage"`
				}
				if json.Unmarshal([]byte(strings.TrimSpace(line)), &x) != nil {
					continue
				}
				for short, i := range byID {
					if short == "" || !(strings.HasPrefix(x.ID, short) || strings.HasPrefix(short, x.ID)) {
						continue
					}
					if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(x.CPUPerc), "%"), 64); err == nil {
						st.Containers[i].CPU = v
					}
					if used, limit, ok := strings.Cut(x.MemUsage, "/"); ok {
						st.Containers[i].MemBytes = parseSize(used)
						if l := parseSize(limit); l > 0 {
							st.Containers[i].MemLimit = l
						}
					}
				}
			}
		}
	}
	sort.Slice(st.Containers, func(a, b int) bool {
		order := map[string]int{"web": 0, "php": 1, "db": 2}
		return order[st.Containers[a].Service] < order[st.Containers[b].Service]
	})
	return st, nil
}

// RestartService restarts one container of a site (web, php or db).
func (m *Manager) RestartService(ctx context.Context, id, svc string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	if !validService(s, svc) {
		return fmt.Errorf("%w: service %q is not part of this site", ErrBadInput, svc)
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if out, err := m.compose(ctx, id, nil, "restart", svc); err != nil {
		return fmt.Errorf("restart %s failed: %s", svc, tail(out))
	}
	return nil
}
