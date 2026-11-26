package sites

// OPS-02: the Server page.
//
// Core services. The core containers (compose project nostek-core: edge, panel, WAF, SFTP, socket proxy) with their
// Docker state, and the host units the panel depends on (Docker, the agent, fail2ban; Nginx and MariaDB with the
// native engine) with their systemd state. Containers, fail2ban, Nginx and MariaDB can be restarted; the agent
// restarts itself a few seconds after the answer; Docker is shown only (restarting it stops every website).
//
// Processes. The top processes by CPU and by memory, read from /proc twice one second apart: name (comm, never the
// command line, which may hold a password), user, CPU share of one core, resident memory, and where it runs (the
// container name, or the systemd unit). Read only.
//
// Drift. For every Docker website the agent renders the files it manages (compose.yml, php.ini, nginx.conf,
// apache.conf) from the website's settings and compares them with the files on disk. A difference shows a short
// line diff; "Write back" rewrites them and restarts the website, with the files of before put back when it does
// not start (the same path as a change of limits). Native websites are not compared.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CoreService is one core container or host unit.
type CoreService struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // container | unit
	State   string `json:"state"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Restart bool   `json:"restart"`
}

type hostUnit struct {
	name, unit string
	restart    bool // can be restarted from the panel
}

// hostUnits are the host services shown.
var hostUnits = []hostUnit{
	{"docker", "docker.service", false},
	{"agent", "nostek-agent.service", true},
	{"fail2ban", "fail2ban.service", true},
}

// CoreServices lists the core containers and host units.
func (m *Manager) CoreServices(ctx context.Context) []CoreService {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out := []CoreService{}
	if raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "ps", "--all", "--filter", "label=com.docker.compose.project="+CoreProject, "--format", "json"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var r struct{ Names, State, Status, Labels string }
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) != nil || !containerName.MatchString(r.Names) {
				continue
			}
			svc := labelValue(r.Labels, "com.docker.compose.service")
			if svc == "" {
				svc = r.Names
			}
			ok := r.State == "running" && !strings.Contains(r.Status, "(unhealthy)")
			out = append(out, CoreService{Name: svc, Kind: "container", State: r.State, OK: ok, Detail: r.Status, Restart: true})
		}
	} else {
		out = append(out, CoreService{Name: "containers", Kind: "container", State: "unknown", Detail: tail(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	units := append([]hostUnit(nil), hostUnits...)
	if m.native != nil {
		units = append(units, hostUnit{"nginx", "nginx.service", true})
		if m.native.MariaDBSocket != "" {
			units = append(units, hostUnit{"mariadb", "mariadb.service", true})
		}
	}
	for _, u := range units {
		st := "unknown"
		if m.fw != nil {
			o, _ := m.fw.Host(ctx, nil, "systemctl", "is-active", u.unit)
			if v := strings.TrimSpace(string(o)); v != "" && len(v) < 20 {
				st = v
			}
		}
		out = append(out, CoreService{Name: u.name, Kind: "unit", State: st, OK: st == "active", Detail: u.unit, Restart: u.restart})
	}
	return out
}

// RestartCore restarts one core container or host unit (not Docker).
func (m *Manager) RestartCore(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for _, u := range hostUnits {
		if u.name != name {
			continue
		}
		if !u.restart || m.fw == nil {
			return fmt.Errorf("%w: %s cannot be restarted from the panel", ErrBadInput, name)
		}
		if name == "agent" { // after this answer: a timer unit restarts the agent in 3 seconds
			unit := "nostek-agent-restart-" + strconv.FormatInt(m.now().Unix(), 10)
			if o, err := m.fw.Host(ctx, nil, "systemd-run", "--unit", unit, "--on-active=3", "/usr/bin/systemctl", "restart", u.unit); err != nil {
				return fmt.Errorf("restart of the agent could not be planned: %s", tail(o))
			}
			return nil
		}
		if o, err := m.fw.Host(ctx, nil, "systemctl", "restart", u.unit); err != nil {
			return fmt.Errorf("restart %s failed: %s", name, tail(o))
		}
		return nil
	}
	if (name == "nginx" || name == "mariadb") && m.native != nil && m.fw != nil {
		if name == "mariadb" && m.native.MariaDBSocket == "" {
			return ErrNotFound
		}
		if o, err := m.fw.Host(ctx, nil, "systemctl", "restart", name+".service"); err != nil {
			return fmt.Errorf("restart %s failed: %s", name, tail(o))
		}
		return nil
	}
	for _, c := range m.CoreServices(ctx) {
		if c.Kind != "container" || c.Name != name || c.State == "unknown" {
			continue
		}
		raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "ps", "--all", "--filter", "label=com.docker.compose.project="+CoreProject,
			"--filter", "label=com.docker.compose.service="+name, "--format", "{{.Names}}")
		if err != nil {
			return fmt.Errorf("docker ps failed: %s", tail(raw))
		}
		for _, n := range strings.Fields(string(raw)) {
			if !containerName.MatchString(n) {
				continue
			}
			if o, err := m.run.Docker(ctx, m.cfg.Data, nil, "restart", n); err != nil {
				return fmt.Errorf("restart %s failed: %s", n, tail(o))
			}
		}
		return nil
	}
	return ErrNotFound
}

// ---------- processes

// Proc is one process.
type Proc struct {
	PID   int     `json:"pid"`
	Name  string  `json:"name"`
	User  string  `json:"user"`
	CPU   float64 `json:"cpu"` // percent of one core
	RSS   int64   `json:"rss"` // bytes
	Mem   float64 `json:"mem"` // percent of the memory
	Where string  `json:"where,omitempty"`
}

// ProcView is the top of the process list.
type ProcView struct {
	At       int64  `json:"at"`
	Cores    int    `json:"cores"`
	MemTotal int64  `json:"mem_total"`
	Count    int    `json:"count"`
	ByCPU    []Proc `json:"by_cpu"`
	ByMem    []Proc `json:"by_mem"`
}

// procWait is the time between the two samples; procBetween runs in between (tests).
var (
	procWait    = time.Second
	procBetween func()
)

const procTop = 15

type procSample struct {
	ticks uint64
	rss   int64
	uid   string
	name  string
	cg    string
}

var pidRe = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

func readProcs(dir string) map[int]procSample {
	out := map[int]procSample{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	page := int64(os.Getpagesize())
	for _, e := range ents {
		if !pidRe.MatchString(e.Name()) {
			continue
		}
		pid, _ := strconv.Atoi(e.Name())
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "stat"))
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: comm may hold spaces and parentheses, so cut at the last ')'
		s := string(b)
		i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if i < 0 || j < i {
			continue
		}
		f := strings.Fields(s[j+1:])
		if len(f) < 22 {
			continue
		}
		ut, _ := strconv.ParseUint(f[11], 10, 64)
		st, _ := strconv.ParseUint(f[12], 10, 64)
		rss, _ := strconv.ParseInt(f[21], 10, 64)
		if rss <= 0 { // kernel threads have no memory of their own
			continue
		}
		p := procSample{ticks: ut + st, rss: rss * page, name: s[i+1 : j]}
		if st, err := os.ReadFile(filepath.Join(dir, e.Name(), "status")); err == nil {
			for l := range strings.SplitSeq(string(st), "\n") {
				if v, ok := strings.CutPrefix(l, "Uid:"); ok {
					if f := strings.Fields(v); len(f) > 0 {
						p.uid = f[0]
					}
				}
			}
		}
		if cg, err := os.ReadFile(filepath.Join(dir, e.Name(), "cgroup")); err == nil {
			p.cg = cgroupPath(string(cg))
		}
		out[pid] = p
	}
	return out
}

// cgroupPath returns the unified (or first) cgroup path of a /proc/<pid>/cgroup file.
func cgroupPath(s string) string {
	first := ""
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" {
			return parts[2]
		}
		if first == "" {
			first = parts[2]
		}
	}
	return first
}

var (
	dockerScopeRe = regexp.MustCompile(`(?:docker-|/docker/)([0-9a-f]{64})`)
	unitRe        = regexp.MustCompile(`/([A-Za-z0-9@_.-]+\.(?:service|scope))$`)
)

// where names a cgroup: the container (by its id) or the systemd unit.
func where(cg string, containers map[string]string) string {
	if m := dockerScopeRe.FindStringSubmatch(cg); m != nil {
		if n, ok := containers[m[1]]; ok {
			return n
		}
		return "container " + m[1][:12]
	}
	if m := unitRe.FindStringSubmatch(cg); m != nil {
		return m[1]
	}
	return ""
}

func memTotal(dir string) int64 {
	b, err := os.ReadFile(filepath.Join(dir, "meminfo"))
	if err != nil {
		return 0
	}
	for l := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseInt(f[0], 10, 64)
				return n << 10
			}
		}
	}
	return 0
}

func cores(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return 1
	}
	n := 0
	for l := range strings.SplitSeq(string(b), "\n") {
		if strings.HasPrefix(l, "cpu") && len(l) > 3 && l[3] >= '0' && l[3] <= '9' {
			n++
		}
	}
	return max(n, 1)
}

// Processes samples /proc twice and returns the top processes by CPU and by memory.
func (m *Manager) Processes(ctx context.Context) (ProcView, error) {
	a := readProcs(procDir)
	if len(a) == 0 {
		return ProcView{}, errors.New("the process list cannot be read")
	}
	t0 := time.Now()
	if procBetween != nil {
		procBetween()
	}
	select {
	case <-ctx.Done():
		return ProcView{}, ctx.Err()
	case <-time.After(procWait):
	}
	b := readProcs(procDir)
	secs := time.Since(t0).Seconds()
	if secs <= 0 {
		secs = 1
	}
	containers := map[string]string{}
	if raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "ps", "--no-trunc", "--format", "{{.ID}} {{.Names}}"); err == nil {
		for l := range strings.SplitSeq(string(raw), "\n") {
			if f := strings.Fields(l); len(f) == 2 && containerName.MatchString(f[1]) {
				containers[f[0]] = f[1]
			}
		}
	}
	v := ProcView{At: m.now().Unix(), Cores: cores(procDir), MemTotal: memTotal(procDir), Count: len(b)}
	users := map[string]string{}
	name := func(uid string) string {
		if n, ok := users[uid]; ok {
			return n
		}
		n := uid
		if u, err := user.LookupId(uid); err == nil {
			n = u.Username
		}
		users[uid] = n
		return n
	}
	all := make([]Proc, 0, len(b))
	for pid, s := range b {
		p := Proc{PID: pid, Name: s.name, User: name(s.uid), RSS: s.rss, Where: where(s.cg, containers)}
		if o, ok := a[pid]; ok && s.ticks >= o.ticks {
			p.CPU = float64(s.ticks-o.ticks) / 100 / secs * 100 // USER_HZ is 100 on Linux
		}
		if v.MemTotal > 0 {
			p.Mem = float64(s.rss) * 100 / float64(v.MemTotal)
		}
		all = append(all, p)
	}
	top := func(less func(x, y Proc) bool) []Proc {
		c := append([]Proc(nil), all...)
		sort.Slice(c, func(i, j int) bool {
			if less(c[i], c[j]) != less(c[j], c[i]) {
				return less(c[i], c[j])
			}
			return c[i].PID < c[j].PID
		})
		return c[:min(procTop, len(c))]
	}
	v.ByCPU = top(func(x, y Proc) bool { return x.CPU > y.CPU || (x.CPU == y.CPU && x.RSS > y.RSS) })
	v.ByMem = top(func(x, y Proc) bool { return x.RSS > y.RSS })
	return v, nil
}

// ---------- drift

// DriftFile is one managed file of a website.
type DriftFile struct {
	File    string `json:"file"`
	State   string `json:"state"` // same | changed | missing
	Added   int    `json:"added,omitempty"`
	Removed int    `json:"removed,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// DriftSite is the drift of one website.
type DriftSite struct {
	Site   string      `json:"site"`
	Domain string      `json:"domain"`
	Native bool        `json:"native,omitempty"`
	Drift  bool        `json:"drift"`
	Files  []DriftFile `json:"files"`
	Error  string      `json:"error,omitempty"`
}

const diffMaxLines = 60

// lineDiff returns a short diff ("- old", "+ new") and the counts. Large files are compared line by line only.
func lineDiff(a, b string) (string, int, int) {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	add, del := 0, 0
	emit := func(s string) {
		if len(out) < diffMaxLines {
			out = append(out, s)
		}
	}
	if len(x) > 3000 || len(y) > 3000 {
		n := max(len(x), len(y))
		for i := range n {
			var l, r string
			if i < len(x) {
				l = x[i]
			}
			if i < len(y) {
				r = y[i]
			}
			if l != r {
				if i < len(x) {
					del++
					emit("- " + l)
				}
				if i < len(y) {
					add++
					emit("+ " + r)
				}
			}
		}
		return strings.Join(out, "\n"), add, del
	}
	// longest common subsequence
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			add++
			emit("+ " + y[j])
			j++
		default:
			del++
			emit("- " + x[i])
			i++
		}
	}
	return strings.Join(out, "\n"), add, del
}

// DriftCheck compares every ready Docker website's managed files with what the agent would write now.
func (m *Manager) DriftCheck() ([]DriftSite, error) {
	list, err := m.List()
	if err != nil {
		return nil, err
	}
	out := []DriftSite{}
	for _, s := range list {
		if s.Phase != "ready" {
			continue
		}
		d := DriftSite{Site: s.ID, Domain: s.Domain, Files: []DriftFile{}}
		if s.IsNative() {
			d.Native = true
			out = append(out, d)
			continue
		}
		prev, next, err := m.managedFiles(s, true)
		if err != nil {
			d.Error = err.Error()
			out = append(out, d)
			continue
		}
		names := make([]string, 0, len(next))
		for n := range next {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			f := DriftFile{File: n, State: "same"}
			old, ok := prev[n]
			switch {
			case !ok:
				f.State, d.Drift = "missing", true
			case string(old) != string(next[n]):
				f.State, d.Drift = "changed", true
				f.Diff, f.Added, f.Removed = lineDiff(string(old), string(next[n]))
			}
			d.Files = append(d.Files, f)
		}
		out = append(out, d)
	}
	return out, nil
}

// DriftFix writes a website's managed files back and restarts it (the files of before come back when it fails).
func (m *Manager) DriftFix(ctx context.Context, id string) error {
	s, err := m.readySite(id)
	if err != nil {
		return err
	}
	if s.IsNative() {
		return fmt.Errorf("%w: native websites are not compared", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	return m.writeManaged(ctx, s, true)
}
