package sites

// Login attempts (SEC-01 wave 3), a brute-force monitor like cPanel's cPHulk history. The edge writes an access
// log (JSON, a few fields, no headers); every minute the agent reads what is new, keeps only POSTs to login pages
// per website and address in hourly buckets for 7 days (<data>/security/logins.json), and empties the log when it
// grows past accessLogMax. Nothing is blocked automatically: the Security page lists the busiest addresses with a
// button that adds them to the IP blocker.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	loginKeep      = 7 * 24 * time.Hour
	loginMaxIPs    = 2000 // per website; the least recent go first
	loginShown     = 25
	accessLogMax   = 32 << 20 // the log is emptied after a read once it is this big
	accessReadMax  = 64 << 20 // at most this much is read in one round
	loginLoopEvery = time.Minute
)

// loginRe matches the login pages of the apps the panel installs and of common frameworks.
var loginRe = regexp.MustCompile(`(?i)^/(wp-login\.php|xmlrpc\.php|administrator(/index\.php)?|user/login|login|signin|sign-in|auth/login|admin/login|api/login|api/auth/login)/?$`)

type loginBucket struct {
	Hour int64 `json:"h"` // unix time of the hour
	N    int   `json:"n"`
	Fail int   `json:"f"`
}

type loginIP struct {
	Buckets  []loginBucket `json:"b"`
	LastSeen int64         `json:"last"`
	LastPath string        `json:"path"`
}

type loginState struct {
	Offset int64                          `json:"offset"`
	Since  int64                          `json:"since"` // when the agent first read the access log
	Sites  map[string]map[string]*loginIP `json:"sites"`
	// SEC-01 wave 3b: the WAF's log, read the same way; the last wafEventsKept events per website
	WAFOffset int64                 `json:"waf_offset"`
	WAF       map[string][]WAFEvent `json:"waf"`
}

type loginStore struct {
	mu    sync.Mutex
	st    loginState
	dirty bool
}

// LoginIPView is one address on the Security page.
type LoginIPView struct {
	IP       string `json:"ip"`
	Day      int    `json:"day"`
	DayFail  int    `json:"day_fail"`
	Week     int    `json:"week"`
	LastSeen int64  `json:"last_seen"`
	LastPath string `json:"last_path"`
	Blocked  bool   `json:"blocked"`
}

// LoginsView is the login-attempts card of the Security page.
type LoginsView struct {
	Active  bool          `json:"active"` // the edge writes the access log
	Since   int64         `json:"since"`
	Day     int           `json:"day"`
	DayFail int           `json:"day_fail"`
	Week    int           `json:"week"`
	IPs     []LoginIPView `json:"ips"`
}

func (m *Manager) accessLog() string {
	if m.cfg.AccessLog != "" {
		return m.cfg.AccessLog
	}
	return filepath.Join(m.cfg.Data, "edge", "logs", "access.log")
}

func (m *Manager) loginFile() string { return filepath.Join(m.cfg.Data, "security", "logins.json") }

// loginDB loads the store once.
func (m *Manager) loginDB() *loginStore {
	m.loginOnce.Do(func() {
		ls := &loginStore{st: loginState{Sites: map[string]map[string]*loginIP{}}}
		if b, err := os.ReadFile(m.loginFile()); err == nil {
			var st loginState
			if json.Unmarshal(b, &st) == nil && st.Sites != nil {
				ls.st = st
			}
		}
		m.logins = ls
	})
	return m.logins
}

// LoginLoop reads the access log every minute until ctx ends.
func (m *Manager) LoginLoop(ctx context.Context, logf func(string, ...any)) {
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := m.ReadLogins(); err != nil {
			logf("login attempts: %v", err)
		}
		t.Reset(loginLoopEvery)
	}
}

// accessEntry is one line of the edge's JSON access log (only the fields the edge keeps).
type accessEntry struct {
	ClientHost       string `json:"ClientHost"`
	RequestHost      string `json:"RequestHost"`
	RequestMethod    string `json:"RequestMethod"`
	RequestPath      string `json:"RequestPath"`
	DownstreamStatus int    `json:"DownstreamStatus"`
	StartUTC         string `json:"StartUTC"`
	// OPS-01: for the visitor statistics
	DownstreamContentSize int64  `json:"DownstreamContentSize"`
	Referer               string `json:"request_Referer"`
	UserAgent             string `json:"request_User-Agent"`
}

// loginFailed tells a failed login from the answer where the app makes that clear: WordPress shows the form
// again (200) when the password is wrong and redirects when it is right; 401, 403, 422 and 429 are refusals.
func loginFailed(path string, status int) bool {
	switch status {
	case 401, 403, 422, 429:
		return true
	case 200:
		return strings.EqualFold(strings.TrimSuffix(path, "/"), "/wp-login.php")
	}
	return false
}

// hostSites maps every name of every website to its id.
func (m *Manager) hostSites() map[string]string {
	out := map[string]string{}
	list, err := m.List()
	if err != nil {
		return out
	}
	for _, s := range list {
		for _, h := range s.Hosts {
			out[strings.ToLower(h.Name)] = s.ID
		}
		if s.Domain != "" {
			out[strings.ToLower(s.Domain)] = s.ID
		}
	}
	return out
}

func (m *Manager) wafLog() string { return filepath.Join(filepath.Dir(m.accessLog()), "waf.log") }

// readWAFLog reads the WAF's new events (ls.mu held).
func (m *Manager) readWAFLog(ls *loginStore) {
	f, err := os.Open(m.wafLog())
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() < ls.st.WAFOffset {
		ls.st.WAFOffset = 0
	}
	if _, err := f.Seek(ls.st.WAFOffset, io.SeekStart); err != nil {
		return
	}
	if ls.st.WAF == nil {
		ls.st.WAF = map[string][]WAFEvent{}
	}
	rd := bufio.NewReaderSize(io.LimitReader(f, accessReadMax), 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			break
		}
		ls.st.WAFOffset += int64(len(line))
		ls.dirty = true
		var e WAFEvent
		if json.Unmarshal(line, &e) != nil || e.Site == "" || e.Rule == 0 {
			continue
		}
		ev := append(ls.st.WAF[e.Site], e)
		if len(ev) > wafEventsKept {
			ev = ev[len(ev)-wafEventsKept:]
		}
		ls.st.WAF[e.Site] = ev
	}
	if fi.Size() > accessLogMax && ls.st.WAFOffset >= fi.Size() {
		if os.Truncate(m.wafLog(), 0) == nil {
			ls.st.WAFOffset = 0
		}
	}
}

// ReadLogins reads what the edge wrote since the last round (the access log, then the WAF's log).
func (m *Manager) ReadLogins() error {
	ls := m.loginDB()
	path := m.accessLog()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // the edge has no access log yet (panel not updated); the WAF comes with the same update
	}
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	m.readWAFLog(ls)
	now := m.now()
	if ls.st.Since == 0 {
		ls.st.Since = now.Unix()
		ls.dirty = true
	}
	if fi.Size() < ls.st.Offset {
		ls.st.Offset = 0 // emptied or replaced
	}
	hosts := m.hostSites()
	if _, err := f.Seek(ls.st.Offset, io.SeekStart); err != nil {
		return err
	}
	stats := m.statDB()
	stats.mu.Lock()
	rd := bufio.NewReaderSize(io.LimitReader(f, accessReadMax), 64<<10)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			break // a line without its newline is read again next round
		}
		ls.st.Offset += int64(len(line))
		ls.dirty = true
		var e accessEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		host := strings.ToLower(e.RequestHost)
		if h, _, err := netSplitHost(host); err == nil {
			host = h
		}
		id, ok := hosts[host]
		if !ok {
			continue
		}
		a, err := netip.ParseAddr(e.ClientHost)
		if err != nil {
			continue
		}
		at := now
		if t, err := time.Parse(time.RFC3339Nano, e.StartUTC); err == nil {
			at = t
		}
		ip := a.Unmap().String()
		// OPS-01: every visit counts in the statistics
		m.statSite(stats, id).add(statEntry{at: at, ip: ip, method: e.RequestMethod, path: e.RequestPath, status: e.DownstreamStatus,
			bytes: e.DownstreamContentSize, ref: e.Referer, ua: e.UserAgent}, func(h string) bool { return hosts[h] == id })
		p, _, _ := strings.Cut(e.RequestPath, "?")
		if e.RequestMethod != "POST" || !loginRe.MatchString(p) {
			continue
		}
		ls.add(id, ip, p, at, loginFailed(p, e.DownstreamStatus))
	}
	stats.mu.Unlock()
	if err := m.saveStats(); err != nil {
		return err
	}
	ls.prune(now)
	if fi.Size() > accessLogMax && ls.st.Offset >= fi.Size() {
		if err := os.Truncate(path, 0); err == nil {
			ls.st.Offset = 0
		}
	}
	if !ls.dirty {
		return nil
	}
	b, err := json.Marshal(ls.st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.loginFile()), 0o700); err != nil {
		return err
	}
	if err := writeFile(m.loginFile(), b, 0o600); err != nil {
		return err
	}
	ls.dirty = false
	return nil
}

// netSplitHost drops a port from a host ("example.com:443"); a bare IPv6 address stays as it is.
func netSplitHost(h string) (string, string, error) {
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[:i], ":") {
		return h[:i], h[i+1:], nil
	}
	return h, "", errors.New("no port")
}

func (ls *loginStore) add(site, ip, path string, at time.Time, failed bool) {
	ips := ls.st.Sites[site]
	if ips == nil {
		ips = map[string]*loginIP{}
		ls.st.Sites[site] = ips
	}
	x := ips[ip]
	if x == nil {
		x = &loginIP{}
		ips[ip] = x
	}
	hour := at.Truncate(time.Hour).Unix()
	if n := len(x.Buckets); n > 0 && x.Buckets[n-1].Hour == hour {
		x.Buckets[n-1].N++
		if failed {
			x.Buckets[n-1].Fail++
		}
	} else {
		b := loginBucket{Hour: hour, N: 1}
		if failed {
			b.Fail = 1
		}
		x.Buckets = append(x.Buckets, b)
		sort.Slice(x.Buckets, func(i, j int) bool { return x.Buckets[i].Hour < x.Buckets[j].Hour })
	}
	if at.Unix() >= x.LastSeen {
		x.LastSeen, x.LastPath = at.Unix(), path
	}
}

// prune drops buckets older than 7 days and keeps at most loginMaxIPs addresses per website.
func (ls *loginStore) prune(now time.Time) {
	cut := now.Add(-loginKeep).Unix()
	for site, ips := range ls.st.Sites {
		for ip, x := range ips {
			k := 0
			for _, b := range x.Buckets {
				if b.Hour >= cut {
					x.Buckets[k] = b
					k++
				}
			}
			x.Buckets = x.Buckets[:k]
			if k == 0 {
				delete(ips, ip)
				ls.dirty = true
			}
		}
		if len(ips) > loginMaxIPs {
			all := make([]string, 0, len(ips))
			for ip := range ips {
				all = append(all, ip)
			}
			sort.Slice(all, func(i, j int) bool { return ips[all[i]].LastSeen > ips[all[j]].LastSeen })
			for _, ip := range all[loginMaxIPs:] {
				delete(ips, ip)
			}
			ls.dirty = true
		}
		if len(ips) == 0 {
			delete(ls.st.Sites, site)
		}
	}
}

// Logins returns the login attempts of a website: totals of the last 24 hours and 7 days and the busiest addresses.
func (m *Manager) Logins(s Site) LoginsView {
	ls := m.loginDB()
	v := LoginsView{IPs: []LoginIPView{}}
	if _, err := os.Stat(m.accessLog()); err == nil {
		v.Active = true
	}
	var blocked []netip.Prefix
	for _, b := range s.BlockIPs {
		if p, err := netip.ParsePrefix(b); err == nil {
			blocked = append(blocked, p)
		} else if a, err := netip.ParseAddr(b); err == nil {
			blocked = append(blocked, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	v.Since = ls.st.Since
	day := m.now().Add(-24 * time.Hour).Unix()
	week := m.now().Add(-loginKeep).Unix()
	for ip, x := range ls.st.Sites[s.ID] {
		r := LoginIPView{IP: ip, LastSeen: x.LastSeen, LastPath: x.LastPath}
		for _, b := range x.Buckets {
			if b.Hour >= week {
				r.Week += b.N
			}
			if b.Hour+3600 > day { // an hour that started before the cut counts in full
				r.Day += b.N
				r.DayFail += b.Fail
			}
		}
		if r.Week == 0 {
			continue
		}
		if a, err := netip.ParseAddr(ip); err == nil {
			for _, p := range blocked {
				if p.Contains(a) {
					r.Blocked = true
				}
			}
		}
		v.Day += r.Day
		v.DayFail += r.DayFail
		v.Week += r.Week
		v.IPs = append(v.IPs, r)
	}
	sort.Slice(v.IPs, func(i, j int) bool {
		a, b := v.IPs[i], v.IPs[j]
		if a.Day != b.Day {
			return a.Day > b.Day
		}
		if a.Week != b.Week {
			return a.Week > b.Week
		}
		return a.IP < b.IP
	})
	if len(v.IPs) > loginShown {
		v.IPs = v.IPs[:loginShown]
	}
	return v
}
