package sites

// Visitor statistics (OPS-01), like cPanel's Metrics (Visitors, Errors, Bandwidth, Raw Access). The agent already
// reads the edge's access log every minute (SEC-01 wave 3); every visit of a website is also counted here, per day
// (WIB) for 31 days: requests, visitors (distinct addresses, bots apart), bandwidth, answers by class, the busiest
// pages, referring sites and errors, and requests per hour. The last raw lines are kept per website. One file per
// website, <data>/stats/<id>.json (0600); addresses of the current day are kept only as short hashes.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statDays    = 31
	statPaths   = 300 // kept per day; the least visited go first
	statRefs    = 100
	statErrors  = 100
	statSeenMax = 200000
	statRawKept = 500
	statShown   = 20
)

// botRe tells automated clients from people by their User-Agent.
var botRe = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|curl|wget|python-requests|go-http-client|headless|monitor|uptime|scan|fetch|http-client|java/|okhttp|^$`)

type statDay struct {
	Day      string         `json:"d"` // 2006-01-02 in WIB
	Req      int            `json:"req"`
	Bot      int            `json:"bot"`
	Bytes    int64          `json:"bytes"`
	S2       int            `json:"s2"`
	S3       int            `json:"s3"`
	S4       int            `json:"s4"`
	S5       int            `json:"s5"`
	Visitors int            `json:"v"`
	Hours    [24]int        `json:"h"`
	Paths    map[string]int `json:"p"`
	Refs     map[string]int `json:"r"`
	Errors   map[string]int `json:"e"` // "404 /path"
}

type siteStats struct {
	Days []*statDay `json:"days"`
	Seen []string   `json:"seen"` // hashed addresses of the last day (people only)
	Raw  []string   `json:"raw"`

	seen  map[string]bool
	dirty bool
}

type statStore struct {
	mu    sync.Mutex
	sites map[string]*siteStats
}

func (m *Manager) statsFile(id string) string { return filepath.Join(m.cfg.Data, "stats", id+".json") }

func (m *Manager) statDB() *statStore {
	m.statOnce.Do(func() { m.stats = &statStore{sites: map[string]*siteStats{}} })
	return m.stats
}

// site loads a website's statistics once (st.mu held).
func (m *Manager) statSite(st *statStore, id string) *siteStats {
	if s, ok := st.sites[id]; ok {
		return s
	}
	s := &siteStats{}
	if b, err := os.ReadFile(m.statsFile(id)); err == nil {
		_ = json.Unmarshal(b, s)
	}
	s.seen = make(map[string]bool, len(s.Seen))
	for _, h := range s.Seen {
		s.seen[h] = true
	}
	st.sites[id] = s
	return s
}

// statEntry is what a visit adds.
type statEntry struct {
	at     time.Time
	ip     string
	method string
	path   string
	status int
	bytes  int64
	ref    string
	ua     string
}

func ipHash(ip string) string {
	h := sha256.Sum256([]byte("nostek-stats:" + ip))
	return hex.EncodeToString(h[:8])
}

// trimTop keeps the n biggest counts of a map once it holds twice as many.
func trimTop(c map[string]int, n int) {
	if len(c) <= 2*n {
		return
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(c))
	for k, v := range c {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v || (all[i].v == all[j].v && all[i].k < all[j].k) })
	for _, x := range all[n:] {
		delete(c, x.k)
	}
}

// refHost is the referring site of a visit, "" for none or the website itself.
func refHost(ref string, own func(string) bool) string {
	if ref == "" || ref == "-" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if own(h) {
		return ""
	}
	return h
}

// add counts one visit (st.mu held).
func (s *siteStats) add(e statEntry, own func(string) bool) {
	day := e.at.In(wib).Format("2006-01-02")
	var d *statDay
	if n := len(s.Days); n > 0 && s.Days[n-1].Day == day {
		d = s.Days[n-1]
	} else if n > 0 && s.Days[n-1].Day > day {
		at := len(s.Days)
		for i, x := range s.Days { // a late line of an earlier day: its day, made in order if needed
			if x.Day == day {
				d = x
				break
			}
			if x.Day > day && at == len(s.Days) {
				at = i
			}
		}
		if d == nil {
			if len(s.Days) >= statDays && at == 0 {
				return // older than every day kept
			}
			d = &statDay{Day: day}
			s.Days = append(s.Days[:at], append([]*statDay{d}, s.Days[at:]...)...)
			if len(s.Days) > statDays {
				s.Days = s.Days[len(s.Days)-statDays:]
			}
		}
	} else {
		if n > 0 {
			s.Days[n-1].Visitors = len(s.seen) // the day is over: its count is final
		}
		d = &statDay{Day: day}
		s.Days = append(s.Days, d)
		if len(s.Days) > statDays {
			s.Days = s.Days[len(s.Days)-statDays:]
		}
		s.seen = map[string]bool{}
	}
	if d.Paths == nil {
		d.Paths, d.Refs, d.Errors = map[string]int{}, map[string]int{}, map[string]int{}
	}
	s.dirty = true
	d.Req++
	d.Bytes += e.bytes
	d.Hours[e.at.In(wib).Hour()]++
	switch {
	case e.status >= 500:
		d.S5++
	case e.status >= 400:
		d.S4++
	case e.status >= 300:
		d.S3++
	default:
		d.S2++
	}
	path, _, _ := strings.Cut(e.path, "?")
	if len(path) > 200 {
		path = path[:200]
	}
	if e.status >= 400 {
		d.Errors[fmt.Sprintf("%d %s", e.status, path)]++
		trimTop(d.Errors, statErrors)
	}
	if botRe.MatchString(e.ua) {
		d.Bot++
	} else {
		if d == s.Days[len(s.Days)-1] && len(s.seen) < statSeenMax {
			s.seen[ipHash(e.ip)] = true
			d.Visitors = len(s.seen)
		}
		if e.status < 400 {
			d.Paths[path]++
			trimTop(d.Paths, statPaths)
		}
		if h := refHost(e.ref, own); h != "" {
			d.Refs[h]++
			trimTop(d.Refs, statRefs)
		}
	}
	ua := e.ua
	if len(ua) > 200 {
		ua = ua[:200]
	}
	ref := e.ref
	if len(ref) > 300 {
		ref = ref[:300]
	}
	p := e.path
	if len(p) > 500 {
		p = p[:500]
	}
	s.Raw = append(s.Raw, fmt.Sprintf("%s - - [%s] %q %d %d %q %q", e.ip, e.at.In(wib).Format("02/Jan/2006:15:04:05 -0700"),
		e.method+" "+p, e.status, e.bytes, ref, ua))
	if len(s.Raw) > statRawKept {
		s.Raw = append([]string(nil), s.Raw[len(s.Raw)-statRawKept:]...)
	}
}

// saveStats writes the websites whose statistics changed.
func (m *Manager) saveStats() error {
	st := m.statDB()
	st.mu.Lock()
	defer st.mu.Unlock()
	var first error
	for id, s := range st.sites {
		if !s.dirty {
			continue
		}
		s.Seen = s.Seen[:0]
		for h := range s.seen {
			s.Seen = append(s.Seen, h)
		}
		sort.Strings(s.Seen)
		b, err := json.Marshal(s)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(m.statsFile(id)), 0o700)
		}
		if err == nil {
			err = writeFile(m.statsFile(id), b, 0o600)
		}
		if err != nil && first == nil {
			first = err
			continue
		}
		s.dirty = false
	}
	return first
}

// dropStats forgets a website's statistics and cron runs (it was deleted permanently).
func (m *Manager) dropStats(id string) {
	st := m.statDB()
	st.mu.Lock()
	delete(st.sites, id)
	st.mu.Unlock()
	_ = os.Remove(m.statsFile(id))
	cs := m.cronDB() // the cron runs go with it
	cs.mu.Lock()
	delete(cs.runs, id)
	delete(cs.loaded, id)
	cs.mu.Unlock()
	_ = os.Remove(m.cronFile(id))
}

// StatDayView is one day of the statistics.
type StatDayView struct {
	Day      string `json:"day"`
	Visitors int    `json:"visitors"`
	Requests int    `json:"requests"`
	Bots     int    `json:"bots"`
	Bytes    int64  `json:"bytes"`
	S2       int    `json:"s2"`
	S3       int    `json:"s3"`
	S4       int    `json:"s4"`
	S5       int    `json:"s5"`
}

// StatCount is one row of a top list.
type StatCount struct {
	Key string `json:"key"`
	N   int    `json:"n"`
}

// MetricsView is the Statistics page of a website.
type MetricsView struct {
	Active bool          `json:"active"` // the edge writes the access log
	Range  int           `json:"range"`  // days summed in the totals and lists
	Total  StatDayView   `json:"total"`
	Days   []StatDayView `json:"days"`  // oldest first, every day of the range (empty days too)
	Hours  [24]int       `json:"hours"` // today, by hour (WIB)
	Pages  []StatCount   `json:"pages"`
	Refs   []StatCount   `json:"refs"`
	Errors []StatCount   `json:"errors"`
	Raw    []string      `json:"raw"` // newest last
}

func topOf(c map[string]int, n int) []StatCount {
	out := make([]StatCount, 0, len(c))
	for k, v := range c {
		out = append(out, StatCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N || (out[i].N == out[j].N && out[i].Key < out[j].Key) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Metrics returns a website's statistics of the last days (1, 7 or 31; others become 7).
func (m *Manager) Metrics(id string, days int) (MetricsView, error) {
	if _, err := m.Get(id); err != nil {
		return MetricsView{}, err
	}
	if days != 1 && days != 31 {
		days = 7
	}
	v := MetricsView{Range: days, Days: []StatDayView{}, Pages: []StatCount{}, Refs: []StatCount{}, Errors: []StatCount{}, Raw: []string{}}
	if _, err := os.Stat(m.accessLog()); err == nil {
		v.Active = true
	}
	st := m.statDB()
	st.mu.Lock()
	defer st.mu.Unlock()
	s := m.statSite(st, id)
	byDay := map[string]*statDay{}
	for _, d := range s.Days {
		byDay[d.Day] = d
	}
	now := m.now().In(wib)
	today := now.Format("2006-01-02")
	pages, refs, errs := map[string]int{}, map[string]int{}, map[string]int{}
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		r := StatDayView{Day: day}
		if d := byDay[day]; d != nil {
			r = StatDayView{Day: day, Visitors: d.Visitors, Requests: d.Req, Bots: d.Bot, Bytes: d.Bytes, S2: d.S2, S3: d.S3, S4: d.S4, S5: d.S5}
			for k, n := range d.Paths {
				pages[k] += n
			}
			for k, n := range d.Refs {
				refs[k] += n
			}
			for k, n := range d.Errors {
				errs[k] += n
			}
			if day == today {
				v.Hours = d.Hours
			}
		}
		v.Days = append(v.Days, r)
		t := &v.Total
		t.Visitors += r.Visitors
		t.Requests += r.Requests
		t.Bots += r.Bots
		t.Bytes += r.Bytes
		t.S2, t.S3, t.S4, t.S5 = t.S2+r.S2, t.S3+r.S3, t.S4+r.S4, t.S5+r.S5
	}
	v.Pages, v.Refs, v.Errors = topOf(pages, statShown), topOf(refs, statShown), topOf(errs, statShown)
	v.Raw = append(v.Raw, s.Raw...)
	return v, nil
}
