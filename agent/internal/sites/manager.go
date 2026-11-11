// Package sites creates and runs WordPress site stacks from the agent's fixed templates.
package sites

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"strings"
	"sync"
	"time"

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
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// Resolver looks up host addresses. Tests replace it.
type Resolver func(ctx context.Context, host string) ([]string, error)

// Config is the agent's fixed configuration.
type Config struct {
	Data        string // /srv/nostek
	PublicIPv4  string // the server's public address; a host is routed only when it resolves here
	PanelDomain string // never usable as a site host
	PreviewZone string // e.g. preview.nostek.dev ("" = no preview addresses)
}

// Manager owns all site directories. One change runs at a time.
type Manager struct {
	cfg     Config
	run     Runner
	resolve Resolver
	now     func() time.Time
	mu      sync.Mutex // serialises changes
	jobs    *Jobs
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
	s.AdminUser = "admin-" + randHex(3)
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
	j.Step("files")
	for _, d := range []string{dir, filepath.Join(dir, "wp"), filepath.Join(dir, "db")} {
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
	files := map[string]string{"compose.yml": cf, "php.ini": PHPIni}
	if s.Server == "nginx" {
		files["nginx.conf"] = NginxConf
	}
	for name, body := range files {
		if err := writeFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := writeSite(dir, s); err != nil {
		fail("files", err, nil)
		return
	}
	j.Step("pull images")
	if out, err := m.compose(ctx, s.ID, nil, "--profile", "tools", "pull", "--quiet"); err != nil {
		fail("pull images", err, out)
		return
	}
	j.Step("start containers")
	if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		fail("start containers", err, out)
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := m.compose(ctx, id, nil, "ps", "--all", "--format", "json")
	if err != nil {
		return "unknown"
	}
	type row struct {
		Service string `json:"Service"`
		State   string `json:"State"`
		Health  string `json:"Health"`
	}
	var rows []row
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			var many []row
			if json.Unmarshal([]byte(line), &many) != nil {
				return "unknown"
			}
			rows = append(rows, many...)
			continue
		}
		var r row
		if json.Unmarshal([]byte(line), &r) != nil {
			return "unknown"
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return "stopped"
	}
	up := 0
	for _, r := range rows {
		if r.State == "running" && r.Health != "unhealthy" && r.Health != "starting" {
			up++
		}
	}
	switch {
	case up == len(rows):
		return "running"
	case up == 0:
		return "stopped"
	default:
		return "partial"
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
	return gone, nil
}
