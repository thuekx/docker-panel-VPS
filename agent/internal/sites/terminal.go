package sites

// TERM-01: a web terminal per website, off by default. A session is a shell inside the website's own container,
// as the user its files belong to (www-data or the app's user), never root and never on the host: the container's
// no-new-privileges and its mounts are the jail, like a chrooted SSH. Native websites have none: their user lives
// on the host. Each session is counted (at most TermPerSite per website, TermTotal in all) and noted in the audit
// log by the API (start, end, duration; never what is typed).

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	TermPerSite = 2
	TermTotal   = 6
	TermIdle    = 15 * time.Minute // no input for this long closes the session
	TermMax     = 2 * time.Hour
)

// TermTarget is where a website's terminal opens.
type TermTarget struct {
	Service string `json:"service"`
	User    string `json:"user"`
	Dir     string `json:"dir"`
}

// TerminalView is the terminal card of a website.
type TerminalView struct {
	Possible bool        `json:"possible"`
	Why      string      `json:"why,omitempty"`
	On       bool        `json:"on"`
	Target   *TermTarget `json:"target,omitempty"`
	Open     int         `json:"open"`
	Max      int         `json:"max"`
	IdleMin  int         `json:"idle_min"`
	MaxMin   int         `json:"max_min"`
}

// termTarget picks the container, user and folder of a website's shell.
func (m *Manager) termTarget(s Site) (TermTarget, error) {
	switch {
	case s.IsNative():
		return TermTarget{}, fmt.Errorf("%w: native websites run on the host; use SFTP", ErrBadInput)
	case phpApp(s):
		cf, err := s.Compose()
		if err != nil {
			return TermTarget{}, err
		}
		var svcs []string
		for k := range phpServices(cf) {
			svcs = append(svcs, k)
		}
		if len(svcs) == 0 {
			return TermTarget{}, fmt.Errorf("%w: the website has no PHP container", ErrBadInput)
		}
		sort.Strings(svcs)
		u := fmt.Sprint(m.uid())
		return TermTarget{Service: svcs[0], User: u + ":" + u, Dir: "/var/www/html"}, nil
	case s.IsRuntime():
		cf, err := s.Compose()
		if err != nil {
			return TermTarget{}, err
		}
		user := ""
		eachService(cf, func(_ int, svc, l string) {
			if v, ok := strings.CutPrefix(l, "    user: "); ok && svc == "app" {
				user = strings.Trim(v, `"`)
			}
		})
		if user == "" || strings.HasPrefix(user, "0") {
			return TermTarget{}, fmt.Errorf("%w: the app container has no user of its own", ErrBadInput)
		}
		return TermTarget{Service: "app", User: user, Dir: "/app"}, nil
	}
	return TermTarget{}, fmt.Errorf("%w: static websites and ready apps have no shell to open", ErrBadInput)
}

// Terminal returns the terminal card of a website.
func (m *Manager) Terminal(id string) (TerminalView, error) {
	s, err := m.readySite(id)
	if err != nil {
		return TerminalView{}, err
	}
	v := TerminalView{On: s.Terminal, Max: TermPerSite, IdleMin: int(TermIdle / time.Minute), MaxMin: int(TermMax / time.Minute)}
	t, err := m.termTarget(s)
	if err != nil {
		v.Why = strings.TrimPrefix(err.Error(), ErrBadInput.Error()+": ")
	} else {
		v.Possible, v.Target = true, &t
	}
	m.termMu.Lock()
	v.Open = m.termN[id]
	m.termMu.Unlock()
	return v, nil
}

// SetTerminal allows or forbids the terminal of a website (open sessions run on until they end).
func (m *Manager) SetTerminal(id string, on bool) error {
	s, err := m.readySite(id)
	if err != nil {
		return err
	}
	if _, err := m.termTarget(s); err != nil && on {
		return err
	}
	if s.Terminal == on {
		return fmt.Errorf("%w: nothing changes", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	s.Terminal = on
	return writeSite(m.dir(id), s)
}

// TermSession is a shell ready to start: Cmd is given a terminal by the caller; Done must be called when it ends.
type TermSession struct {
	Cmd    *exec.Cmd
	Target TermTarget
	Done   func()
}

// OpenTerminal checks a website and its session count and returns the shell command of a new session.
func (m *Manager) OpenTerminal(ctx context.Context, id string) (*TermSession, error) {
	s, err := m.readySite(id)
	if err != nil {
		return nil, err
	}
	if !s.Terminal {
		return nil, fmt.Errorf("%w: the terminal of this website is off", ErrBadInput)
	}
	t, err := m.termTarget(s)
	if err != nil {
		return nil, err
	}
	m.termMu.Lock()
	total := 0
	for _, n := range m.termN {
		total += n
	}
	if m.termN[id] >= TermPerSite || total >= TermTotal {
		m.termMu.Unlock()
		return nil, fmt.Errorf("%w: too many open terminals (at most %d per website, %d in all)", ErrBusy, TermPerSite, TermTotal)
	}
	if m.termN == nil {
		m.termN = map[string]int{}
	}
	m.termN[id]++
	m.termMu.Unlock()
	done := func() {
		m.termMu.Lock()
		if m.termN[id]--; m.termN[id] <= 0 {
			delete(m.termN, id)
		}
		m.termMu.Unlock()
	}
	dir := m.dir(id)
	shell := "cd " + t.Dir + " 2>/dev/null; if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"
	args := []string{"compose", "--project-directory", dir, "-p", Project(id), "-f", dir + "/compose.yml",
		"exec", "-u", t.User, "-e", "TERM=xterm-256color", "-e", "HOME=/tmp", "-e", "PS1=\\u@" + id + ":\\w\\$ ", t.Service, "sh", "-c", shell}
	mk := m.termCmd
	if mk == nil {
		mk = func(ctx context.Context, dir string, args []string) *exec.Cmd {
			bin := "docker"
			if r, ok := m.run.(ExecRunner); ok && r.Bin != "" {
				bin = r.Bin
			}
			c := exec.CommandContext(ctx, bin, args...)
			c.Dir = dir
			return c
		}
	}
	return &TermSession{Cmd: mk(ctx, dir, args), Target: t, Done: done}, nil
}

// UseTermCmd makes the shell of terminal sessions with f (tests: a local shell in place of docker compose exec).
func (m *Manager) UseTermCmd(f func(ctx context.Context, dir string, args []string) *exec.Cmd) {
	m.termCmd = f
}
