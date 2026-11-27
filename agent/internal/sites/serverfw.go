package sites

// Server security (SRV-01): the server firewall (ufw) and fail2ban from the panel.
//
// Firewall. Rules are added or removed one at a time, each as a trial: the agent keeps ufw's rule files from before
// the change and puts them back by itself after fwTrialFor unless the change is confirmed (the panel asks for the
// confirmation from a session that started after the change, so the admin proves they can still sign in). SSH
// ports (from sshd -T) can never be closed or denied, and the rules other parts of the panel keep (DB-02 database
// ports) are shown but cannot be removed here. A trial outlives an agent restart: it is resumed at start.
//
// fail2ban. The panel and the SFTP server write a line per failed sign-in to a file on the host; two jails read
// them and ban in the DOCKER-USER chain, because both are reached through Docker's published ports, which pass by
// ufw and the INPUT chain (SRV-1 = A). The page lists every jail with its banned addresses, unbans one, and keeps an
// allow-list (ignoreip) that no jail bans.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fwTrialFor   = 10 * time.Minute
	fwMaxAllow   = 50
	authFailFile = "auth-fail.log"
)

// FWRule is one ufw rule as `ufw status numbered` shows it.
type FWRule struct {
	N       int    `json:"n"`
	To      string `json:"to"`
	Action  string `json:"action"`
	From    string `json:"from"`
	Comment string `json:"comment,omitempty"`
	V6      bool   `json:"v6"`
	Locked  string `json:"locked,omitempty"` // ssh | panel | web: why it cannot be removed here
}

// FWTrial is a firewall change waiting for its confirmation.
type FWTrial struct {
	Started  int64  `json:"started"`
	Deadline int64  `json:"deadline"`
	What     string `json:"what"`
}

// Jail is one fail2ban jail.
type Jail struct {
	Name   string   `json:"name"`
	Banned []string `json:"banned"`
	Failed int      `json:"failed"` // failures seen now
	Total  int      `json:"total"`  // bans since fail2ban started
}

// ServerSecurity is the Server security page.
type ServerSecurity struct {
	Active   bool     `json:"active"`
	Rules    []FWRule `json:"rules"`
	SSHPorts []int    `json:"ssh_ports"`
	Trial    *FWTrial `json:"trial,omitempty"`
	FWError  string   `json:"fw_error,omitempty"`
	Jails    []Jail   `json:"jails"`
	Allow    []string `json:"allow"`
	F2BError string   `json:"f2b_error,omitempty"`
}

// FWChange is one firewall change.
type FWChange struct {
	Op    string `json:"op"`    // allow | deny | delete
	Port  string `json:"port"`  // 8080 or 8000:8100
	Proto string `json:"proto"` // tcp | udp | both
	From  string `json:"from"`  // "" = anywhere, or an address or network
	Rule  int    `json:"rule"`  // delete: the rule number
	Match string `json:"match"` // delete: the rule as listed (To|Action|From), so a renumbered list is refused
}

type fwState struct {
	mu    sync.Mutex
	timer *time.Timer
}

func (m *Manager) fwDir() string { return filepath.Join(m.cfg.Data, "firewall") }

func (m *Manager) ufwEtc() string {
	if m.srvEtc != "" {
		return filepath.Join(m.srvEtc, "ufw")
	}
	return "/etc/ufw"
}

func (m *Manager) f2bEtc() string {
	if m.srvEtc != "" {
		return filepath.Join(m.srvEtc, "fail2ban")
	}
	return "/etc/fail2ban"
}

// OnFirewallUndo is told when a firewall trial is undone (the audit log).
func (m *Manager) OnFirewallUndo(f func(why string, err error)) { m.onFWUndo = f }

// UseServerEtc points the firewall and fail2ban files at a test folder (tests only).
func (m *Manager) UseServerEtc(dir string) { m.srvEtc = dir }

var ufwLine = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)(?: (?:IN|OUT|FWD))?\s+(.+?)(?:\s+#\s*(.*))?$`)

func parseUFW(out string) (bool, []FWRule) {
	active := false
	var rules []FWRule
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, " \r")
		if strings.HasPrefix(l, "Status: active") {
			active = true
		}
		p := ufwLine.FindStringSubmatch(l)
		if p == nil {
			continue
		}
		n, _ := strconv.Atoi(p[1])
		r := FWRule{N: n, To: strings.TrimSpace(p[2]), Action: p[3], From: strings.TrimSpace(p[4]), Comment: strings.TrimSpace(p[5])}
		if strings.HasSuffix(r.To, "(v6)") || strings.HasSuffix(r.From, "(v6)") {
			r.V6 = true
			r.To = strings.TrimSpace(strings.TrimSuffix(r.To, "(v6)"))
			r.From = strings.TrimSpace(strings.TrimSuffix(r.From, "(v6)"))
		}
		rules = append(rules, r)
	}
	return active, rules
}

// rulePorts gives the ports a rule's "To" names ("20022/tcp", "8000:8100/udp", "22", "Anywhere" = every port).
func rulePorts(to string) (lo, hi int, all bool) {
	f := strings.Fields(to)
	if len(f) == 0 {
		return 0, 0, true
	}
	p := f[len(f)-1]
	if p == "Anywhere" || strings.Contains(p, ".") || strings.Contains(p, ":") && strings.Count(p, ":") > 1 {
		return 0, 0, true
	}
	p, _, _ = strings.Cut(p, "/")
	a, b, rng := strings.Cut(p, ":")
	lo, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false // a named application profile: not a port
	}
	hi = lo
	if rng {
		if hi, err = strconv.Atoi(b); err != nil {
			return 0, 0, false
		}
	}
	return lo, hi, false
}

func (m *Manager) sshPorts(ctx context.Context) []int {
	out, err := m.fw.Host(ctx, nil, "sshd", "-T")
	set := map[int]bool{}
	if err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[0] == "port" {
				if p, err := strconv.Atoi(f[1]); err == nil && p > 0 && p < 65536 {
					set[p] = true
				}
			}
		}
	}
	if len(set) == 0 {
		set[22] = true
	}
	var ports []int
	for p := range set {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

func lockOf(r FWRule, ssh []int) string {
	if strings.HasPrefix(r.Comment, "nostek db ") {
		return "panel"
	}
	lo, hi, all := rulePorts(r.To)
	if all && r.Action != "ALLOW" {
		return "ssh" // a deny for every port would close SSH too
	}
	for _, p := range ssh {
		if !all && p >= lo && p <= hi {
			return "ssh"
		}
	}
	covers := func(p int) bool { return !all && lo <= p && p <= hi }
	if (covers(80) || covers(443)) && r.Action == "ALLOW" && r.From == "Anywhere" {
		return "web"
	}
	return ""
}

// ServerSecurityView reads ufw and fail2ban.
func (m *Manager) ServerSecurityView(ctx context.Context) ServerSecurity {
	v := ServerSecurity{Rules: []FWRule{}, Jails: []Jail{}, Allow: []string{}}
	if m.fw == nil {
		v.FWError, v.F2BError = "no host tools", "no host tools"
		return v
	}
	v.SSHPorts = m.sshPorts(ctx)
	out, err := m.fw.Host(ctx, nil, "ufw", "status", "numbered")
	if err != nil {
		v.FWError = "ufw: " + firstLine(out)
	} else {
		v.Active, v.Rules = parseUFW(string(out))
		for i := range v.Rules {
			v.Rules[i].Locked = lockOf(v.Rules[i], v.SSHPorts)
		}
	}
	v.Trial = m.fwTrial()
	v.Jails, v.F2BError = m.jails(ctx)
	v.Allow = m.f2bAllow()
	return v
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

var jailRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (m *Manager) jails(ctx context.Context) ([]Jail, string) {
	out, err := m.fw.Host(ctx, nil, "fail2ban-client", "status")
	if err != nil {
		return []Jail{}, "fail2ban: " + firstLine(out)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if _, list, ok := strings.Cut(l, "Jail list:"); ok {
			for _, n := range strings.Split(list, ",") {
				if n = strings.TrimSpace(n); jailRe.MatchString(n) {
					names = append(names, n)
				}
			}
		}
	}
	jails := []Jail{}
	for _, n := range names {
		j := Jail{Name: n, Banned: []string{}}
		out, err := m.fw.Host(ctx, nil, "fail2ban-client", "status", n)
		if err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				k, val, ok := strings.Cut(l, ":")
				if !ok {
					continue
				}
				k, val = strings.Trim(k, " |`-\t"), strings.TrimSpace(val)
				switch k {
				case "Currently failed":
					j.Failed, _ = strconv.Atoi(val)
				case "Total banned":
					j.Total, _ = strconv.Atoi(val)
				case "Banned IP list":
					for _, a := range strings.Fields(val) {
						if _, err := netip.ParseAddr(a); err == nil {
							j.Banned = append(j.Banned, a)
						}
					}
				}
			}
		}
		jails = append(jails, j)
	}
	return jails, ""
}

// ---------- firewall changes ----------

var portRe = regexp.MustCompile(`^([0-9]{1,5})(?::([0-9]{1,5}))?$`)

func (m *Manager) fwTrial() *FWTrial {
	b, err := os.ReadFile(filepath.Join(m.fwDir(), "trial.json"))
	if err != nil {
		return nil
	}
	var t FWTrial
	if json.Unmarshal(b, &t) != nil {
		return nil
	}
	return &t
}

func normFrom(from string) (string, error) {
	from = strings.TrimSpace(from)
	if from == "" || strings.EqualFold(from, "any") {
		return "", nil
	}
	if a, err := netip.ParseAddr(from); err == nil {
		return a.String(), nil
	}
	p, err := netip.ParsePrefix(from)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not an address or network", ErrBadInput, from)
	}
	return p.Masked().String(), nil
}

// FirewallChange applies one change as a trial.
func (m *Manager) FirewallChange(ctx context.Context, c FWChange) (FWTrial, error) {
	if m.fw == nil {
		return FWTrial{}, errors.New("no host tools")
	}
	if !m.mu.TryLock() {
		return FWTrial{}, ErrBusy
	}
	defer m.mu.Unlock()
	if m.fwTrial() != nil {
		return FWTrial{}, fmt.Errorf("%w: confirm or undo the change on trial first", ErrBadInput)
	}
	ssh := m.sshPorts(ctx)
	var args []string
	what := ""
	switch c.Op {
	case "allow", "deny":
		p := portRe.FindStringSubmatch(c.Port)
		if p == nil {
			return FWTrial{}, fmt.Errorf("%w: a port is 1-65535, or a range like 8000:8100", ErrBadInput)
		}
		lo, _ := strconv.Atoi(p[1])
		hi := lo
		if p[2] != "" {
			hi, _ = strconv.Atoi(p[2])
		}
		if lo < 1 || hi > 65535 || hi < lo || hi-lo > 1000 {
			return FWTrial{}, fmt.Errorf("%w: a port is 1-65535, a range at most 1000 ports", ErrBadInput)
		}
		if c.Proto != "tcp" && c.Proto != "udp" && c.Proto != "both" {
			return FWTrial{}, fmt.Errorf("%w: protocol is tcp, udp or both", ErrBadInput)
		}
		if p[2] != "" && c.Proto == "both" {
			return FWTrial{}, fmt.Errorf("%w: a port range needs tcp or udp", ErrBadInput)
		}
		from, err := normFrom(c.From)
		if err != nil {
			return FWTrial{}, err
		}
		if c.Op == "deny" {
			for _, s := range ssh {
				if s >= lo && s <= hi {
					return FWTrial{}, fmt.Errorf("%w: port %d is SSH; it cannot be closed from the panel", ErrBadInput, s)
				}
			}
		}
		port := c.Port
		args = []string{c.Op}
		if from != "" {
			args = append(args, "from", from, "to", "any", "port", port)
			if c.Proto != "both" {
				args = append(args, "proto", c.Proto)
			}
		} else if c.Proto == "both" {
			args = append(args, port)
		} else {
			args = append(args, port+"/"+c.Proto)
		}
		args = append(args, "comment", "nostek panel")
		what = c.Op + " " + port + " " + c.Proto
		if from != "" {
			what += " from " + from
		}
	case "delete":
		out, err := m.fw.Host(ctx, nil, "ufw", "status", "numbered")
		if err != nil {
			return FWTrial{}, errors.New("ufw: " + firstLine(out))
		}
		_, rules := parseUFW(string(out))
		var r *FWRule
		for i := range rules {
			if rules[i].N == c.Rule {
				r = &rules[i]
			}
		}
		if r == nil || r.To+"|"+r.Action+"|"+r.From != c.Match {
			return FWTrial{}, fmt.Errorf("%w: the rule list changed; reload the page", ErrBadInput)
		}
		if l := lockOf(*r, ssh); l != "" {
			return FWTrial{}, fmt.Errorf("%w: this rule cannot be removed from the panel (%s)", ErrBadInput, l)
		}
		args = []string{"--force", "delete", strconv.Itoa(c.Rule)}
		what = "remove " + r.Action + " " + r.To + " from " + r.From
		if r.V6 {
			what += " (v6)"
		}
	default:
		return FWTrial{}, fmt.Errorf("%w: unknown change", ErrBadInput)
	}
	if err := m.fwSnapshot(); err != nil {
		return FWTrial{}, err
	}
	if out, err := m.fw.Host(ctx, nil, "ufw", args...); err != nil {
		_ = m.fwRestore(ctx)
		return FWTrial{}, fmt.Errorf("ufw refused the change: %s", firstLine(out))
	}
	now := m.now()
	t := FWTrial{Started: now.Unix(), Deadline: now.Add(fwTrialFor).Unix(), What: what}
	b, _ := json.Marshal(t)
	if err := os.WriteFile(filepath.Join(m.fwDir(), "trial.json"), b, 0o600); err != nil {
		_ = m.fwRestore(ctx)
		return FWTrial{}, err
	}
	m.fwSchedule(fwTrialFor)
	return t, nil
}

// fwSnapshot keeps ufw's rule files from before a change.
func (m *Manager) fwSnapshot() error {
	dir := filepath.Join(m.fwDir(), "before")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(m.fwDir(), 0o700)
	for _, f := range []string{"user.rules", "user6.rules"} {
		b, err := os.ReadFile(filepath.Join(m.ufwEtc(), f))
		if err != nil {
			return fmt.Errorf("cannot keep ufw's %s: %w", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// fwRestore puts the kept rule files back and reloads ufw; the trial ends.
func (m *Manager) fwRestore(ctx context.Context) error {
	dir := filepath.Join(m.fwDir(), "before")
	for _, f := range []string{"user.rules", "user6.rules"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		dst := filepath.Join(m.ufwEtc(), f)
		if err := os.WriteFile(dst+".nostek", b, 0o640); err != nil {
			return err
		}
		if err := os.Rename(dst+".nostek", dst); err != nil {
			return err
		}
	}
	out, err := m.fw.Host(ctx, nil, "ufw", "reload")
	_ = os.Remove(filepath.Join(m.fwDir(), "trial.json"))
	_ = os.RemoveAll(dir)
	if err != nil {
		return errors.New("ufw reload: " + firstLine(out))
	}
	return nil
}

func (m *Manager) fwSchedule(d time.Duration) {
	m.fwSt.mu.Lock()
	defer m.fwSt.mu.Unlock()
	if m.fwSt.timer != nil {
		m.fwSt.timer.Stop()
	}
	m.fwSt.timer = time.AfterFunc(d, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = m.FirewallUndo(ctx, "the change was not confirmed in time")
	})
}

// FirewallConfirm keeps the change on trial.
func (m *Manager) FirewallConfirm() error {
	if m.fwTrial() == nil {
		return fmt.Errorf("%w: no change is on trial", ErrBadInput)
	}
	m.fwSt.mu.Lock()
	if m.fwSt.timer != nil {
		m.fwSt.timer.Stop()
	}
	m.fwSt.mu.Unlock()
	_ = os.Remove(filepath.Join(m.fwDir(), "trial.json"))
	return os.RemoveAll(filepath.Join(m.fwDir(), "before"))
}

// FirewallUndo puts the rules from before the change back.
func (m *Manager) FirewallUndo(ctx context.Context, why string) error {
	if m.fwTrial() == nil {
		return fmt.Errorf("%w: no change is on trial", ErrBadInput)
	}
	m.fwSt.mu.Lock()
	if m.fwSt.timer != nil {
		m.fwSt.timer.Stop()
	}
	m.fwSt.mu.Unlock()
	err := m.fwRestore(ctx)
	if m.onFWUndo != nil {
		m.onFWUndo(why, err)
	}
	return err
}

// ResumeFirewallTrial is called at agent start: a trial past its deadline is undone now, another one later.
func (m *Manager) ResumeFirewallTrial(ctx context.Context) {
	t := m.fwTrial()
	if t == nil || m.fw == nil {
		return
	}
	left := time.Unix(t.Deadline, 0).Sub(m.now())
	if left <= 0 {
		_ = m.FirewallUndo(ctx, "the change was not confirmed before the agent restarted")
		return
	}
	m.fwSchedule(left)
}

// ---------- fail2ban ----------

// Unban lifts one ban.
func (m *Manager) Unban(ctx context.Context, jail, addr string) error {
	if !jailRe.MatchString(jail) {
		return fmt.Errorf("%w: unknown jail", ErrBadInput)
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return fmt.Errorf("%w: not an address", ErrBadInput)
	}
	if out, err := m.fw.Host(ctx, nil, "fail2ban-client", "set", jail, "unbanip", a.String()); err != nil {
		return errors.New("fail2ban: " + firstLine(out))
	}
	return nil
}

func (m *Manager) f2bAllowFile() string {
	return filepath.Join(m.f2bEtc(), "jail.d", "nostek-allow.local")
}

func (m *Manager) f2bAllow() []string {
	b, err := os.ReadFile(m.f2bAllowFile())
	out := []string{}
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ignoreip"); ok {
			v = strings.TrimLeft(v, " =")
			for _, a := range strings.Fields(v) {
				if a != "127.0.0.1/8" && a != "::1" {
					out = append(out, a)
				}
			}
		}
	}
	return out
}

// SetFail2banAllow writes the allow-list (addresses or networks no jail bans) and reloads fail2ban.
func (m *Manager) SetFail2banAllow(ctx context.Context, list []string) ([]string, error) {
	if len(list) > fwMaxAllow {
		return nil, fmt.Errorf("%w: at most %d entries", ErrBadInput, fwMaxAllow)
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		n, err := normFrom(a)
		if err != nil || n == "" {
			return nil, fmt.Errorf("%w: %q is not an address or network", ErrBadInput, a)
		}
		if p, err := netip.ParsePrefix(n); err == nil && (p.Addr().Is4() && p.Bits() < 8 || !p.Addr().Is4() && p.Bits() < 16) {
			return nil, fmt.Errorf("%w: %s is too wide", ErrBadInput, n)
		}
		if !seen[n] {
			seen[n] = true
			clean = append(clean, n)
		}
	}
	var b bytes.Buffer
	b.WriteString("# Written by the Nostek agent (Settings → Server security). Addresses no jail bans.\n[DEFAULT]\n")
	b.WriteString("ignoreip = 127.0.0.1/8 ::1")
	for _, a := range clean {
		b.WriteString(" " + a)
	}
	b.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(m.f2bAllowFile()), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(m.f2bAllowFile()+".tmp", b.Bytes(), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(m.f2bAllowFile()+".tmp", m.f2bAllowFile()); err != nil {
		return nil, err
	}
	if out, err := m.fw.Host(ctx, nil, "fail2ban-client", "reload"); err != nil {
		return nil, errors.New("fail2ban reload: " + firstLine(out))
	}
	return clean, nil
}

// f2bJails are the panel's own jails (SRV-1 = A): one line per failed sign-in, banned in DOCKER-USER.
const f2bFilter = `# Written by the Nostek agent. One line per failed sign-in, written by the panel or the SFTP server.
[Definition]
failregex = ^\S+ nostek-auth fail ip=<HOST> service=\S+\s*$
ignoreregex =
`

func (m *Manager) f2bJailText() string {
	j := "# Written by the Nostek agent (SRV-01). The panel and SFTP are reached through Docker's published ports,\n" +
		"# so bans go into the DOCKER-USER chain (ufw and INPUT do not see that traffic).\n"
	for _, x := range []struct{ name, log string }{
		{"nostek-panel", filepath.Join(m.panelDir(), authFailFile)},
		{"nostek-sftp", filepath.Join(m.cfg.Data, "sftp-state", authFailFile)},
	} {
		j += fmt.Sprintf("\n[%s]\nenabled = true\nfilter = nostek-auth\nbackend = auto\nlogpath = %s\nmaxretry = 10\nfindtime = 10m\nbantime = 1h\nbanaction = iptables-allports[chain=DOCKER-USER]\n", x.name, x.log)
	}
	return j
}

// SyncFail2ban writes the panel's jails when fail2ban is installed, and reloads it when they changed.
func (m *Manager) SyncFail2ban(ctx context.Context) error {
	if m.fw == nil {
		return nil
	}
	if _, err := os.Stat(m.f2bEtc()); err != nil {
		return nil // fail2ban not installed
	}
	for _, p := range []string{filepath.Join(m.panelDir(), authFailFile), filepath.Join(m.cfg.Data, "sftp-state", authFailFile)} {
		if _, err := os.Stat(p); err != nil {
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				f.Close()
			}
		}
	}
	// the panel writes its own file as its user
	uid := m.cfg.PanelUID
	if uid == 0 {
		uid = 65532
	}
	_ = os.Chown(filepath.Join(m.panelDir(), authFailFile), uid, uid)
	changed := false
	for _, f := range []struct{ path, text string }{
		{filepath.Join(m.f2bEtc(), "filter.d", "nostek-auth.conf"), f2bFilter},
		{filepath.Join(m.f2bEtc(), "jail.d", "nostek-panel.local"), m.f2bJailText()},
	} {
		if b, err := os.ReadFile(f.path); err == nil && string(b) == f.text {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.path, []byte(f.text), 0o644); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if out, err := m.fw.Host(ctx, nil, "fail2ban-client", "reload"); err != nil {
		return errors.New("fail2ban reload: " + firstLine(out))
	}
	return nil
}
