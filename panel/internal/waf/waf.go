// Package waf is the web application firewall of the edge (SEC-01 wave 3b): Coraza with the OWASP Core Rule Set,
// as a reverse proxy between the edge and a website. The edge sends a website's visits here only while its WAF is
// on; the host of the visit names the website. Each website has its own rule set (mode, paranoia level, removed
// rules, paths that are not checked), read from <config>/<id>.json, which nostek-agent writes. Matched rules go to
// a JSON log the agent reads for the Security page.
package waf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	txhttp "github.com/corazawaf/coraza/v3/http"
	"github.com/corazawaf/coraza/v3/types"
)

// Site is one website's WAF settings, written by nostek-agent.
type Site struct {
	Site     string   `json:"site"`
	Hosts    []string `json:"hosts"`
	Upstream string   `json:"upstream"` // http://np-site-x-web-1:80 or the native gateway
	Mode     string   `json:"mode"`     // detect | block
	Paranoia int      `json:"paranoia"` // 1 or 2
	Exclude  []int    `json:"exclude"`  // rule ids that do not run
	Skip     []string `json:"skip"`     // path prefixes that are not checked
}

var (
	siteRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	skipRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
)

// Check refuses a settings file that is not one the agent writes.
func (s Site) Check() error {
	if !siteRe.MatchString(s.Site) || len(s.Hosts) == 0 {
		return errors.New("bad site")
	}
	u, err := url.Parse(s.Upstream)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.Path != "" {
		return errors.New("bad upstream")
	}
	if s.Mode != "detect" && s.Mode != "block" {
		return errors.New("bad mode")
	}
	if s.Paranoia < 1 || s.Paranoia > 2 {
		return errors.New("bad paranoia level")
	}
	for _, id := range s.Exclude {
		if id < 910000 || id > 999999 {
			return errors.New("bad rule id")
		}
	}
	for _, p := range s.Skip {
		if !skipRe.MatchString(p) || strings.Contains(p, "..") {
			return errors.New("bad path")
		}
	}
	return nil
}

// Directives are the Coraza directives of a website.
func (s Site) Directives() string {
	engine := "DetectionOnly"
	if s.Mode == "block" {
		engine = "On"
	}
	var b strings.Builder
	b.WriteString("Include @coraza.conf-recommended\n")
	fmt.Fprintf(&b, "SecRuleEngine %s\n", engine)
	// bodies: the first 128 KB is checked (in memory); the rest of a larger body passes unchecked. CRS on a long
	// form field costs about 6 s per MB, so a larger limit would let one visit hold a CPU for seconds
	b.WriteString("SecRequestBodyAccess On\nSecRequestBodyLimit 131072\nSecRequestBodyInMemoryLimit 131072\n" +
		"SecRequestBodyLimitAction ProcessPartial\nSecResponseBodyAccess Off\nSecAuditEngine Off\nSecTmpDir /tmp\n")
	b.WriteString("Include @crs-setup.conf.example\n")
	fmt.Fprintf(&b, "SecAction \"id:900000,phase:1,pass,t:none,nolog,setvar:tx.blocking_paranoia_level=%d\"\n", s.Paranoia)
	for i, p := range s.Skip {
		op := "@beginsWith"
		if p == "/" {
			op = "@unconditionalMatch"
		}
		fmt.Fprintf(&b, "SecRule REQUEST_FILENAME \"%s %s\" \"id:%d,phase:1,pass,nolog,ctl:ruleEngine=Off\"\n", op, p, 1000+i)
	}
	b.WriteString("Include @owasp_crs/*.conf\n")
	ids := append([]int(nil), s.Exclude...)
	sort.Ints(ids)
	for _, id := range ids {
		fmt.Fprintf(&b, "SecRuleRemoveById %d\n", id)
	}
	return b.String()
}

// Event is one matched rule, a line of the WAF log.
type Event struct {
	Time   int64  `json:"t"`
	Site   string `json:"site"`
	IP     string `json:"ip"`
	URI    string `json:"uri"`
	Rule   int    `json:"rule"`
	Msg    string `json:"msg"`
	Data   string `json:"data,omitempty"`
	Action string `json:"action"` // blocked | detected
}

// eventsPerMinute caps the log lines of one website, so an attack cannot fill the disk.
const eventsPerMinute = 120

type logger struct {
	mu   sync.Mutex
	w    io.Writer
	seen map[string]int // site -> lines this minute
	min  int64
	now  func() time.Time
}

func (l *logger) write(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.now().Unix() / 60
	if m != l.min {
		l.min, l.seen = m, map[string]int{}
	}
	if l.seen[e.Site] >= eventsPerMinute {
		return
	}
	l.seen[e.Site]++
	b, _ := json.Marshal(e)
	_, _ = l.w.Write(append(b, '\n'))
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// skipRule tells the rules that only count or decide (not a finding of their own) from the attack rules;
// 949110 (inbound score exceeded) is kept as the line that says a visit was blocked.
func skipRule(id int) bool {
	return id < 910000 || (id >= 949000 && id < 950000 && id != 949110) || id >= 959000
}

type site struct {
	cfg   Site
	stamp string
	h     http.Handler
}

// Server proxies visits to the websites through their WAF.
type Server struct {
	dir   string
	log   *logger
	mu    sync.RWMutex
	hosts map[string]*site
	sites map[string]*site
}

// New returns a server that reads its websites from dir and writes matched rules to w.
func New(dir string, w io.Writer) *Server {
	return &Server{dir: dir, log: &logger{w: w, now: time.Now}, hosts: map[string]*site{}, sites: map[string]*site{}}
}

func (sv *Server) build(cfg Site, stamp string) (*site, error) {
	block := cfg.Mode == "block"
	w, err := coraza.NewWAF(coraza.NewWAFConfig().WithRootFS(coreruleset.FS).WithDirectives(cfg.Directives()).
		WithErrorCallback(func(mr types.MatchedRule) {
			id := mr.Rule().ID()
			if skipRule(id) {
				return
			}
			a := "detected"
			if block && id == 949110 {
				a = "blocked"
			}
			sv.log.write(Event{Time: time.Now().Unix(), Site: cfg.Site, IP: mr.ClientIPAddress(), URI: cut(mr.URI(), 300),
				Rule: id, Msg: cut(mr.Message(), 200), Data: cut(mr.Data(), 200), Action: a})
		}))
	if err != nil {
		return nil, err
	}
	up, _ := url.Parse(cfg.Upstream)
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(up)
			r.Out.Host = r.In.Host // the website routes by its own names
			// the edge's forwarded headers pass unchanged: the website sees the visitor, not this proxy
			for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port", "X-Real-Ip"} {
				if v := r.In.Header.Values(h); len(v) > 0 {
					r.Out.Header[h] = v
				}
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "website not reachable", http.StatusBadGateway)
		},
	}
	return &site{cfg: cfg, stamp: stamp, h: txhttp.WrapHandler(w, rp)}, nil
}

// Load reads the settings files again and rebuilds the websites whose file changed.
func (sv *Server) Load() error {
	ents, err := os.ReadDir(sv.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	sv.mu.RLock()
	old := sv.sites
	sv.mu.RUnlock()
	sites := map[string]*site{}
	var errs []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		stamp := fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
		id := strings.TrimSuffix(name, ".json")
		if s, ok := old[id]; ok && s.stamp == stamp {
			sites[id] = s
			continue
		}
		var cfg Site
		b, err := os.ReadFile(filepath.Join(sv.dir, name))
		if err == nil {
			err = json.Unmarshal(b, &cfg)
		}
		if err == nil {
			err = cfg.Check()
		}
		if err == nil && cfg.Site != id {
			err = errors.New("file name and site differ")
		}
		var s *site
		if err == nil {
			s, err = sv.build(cfg, stamp)
		}
		if err != nil {
			errs = append(errs, name+": "+err.Error())
			if p, ok := old[id]; ok {
				sites[id] = p // keep the previous rules of a website whose new file is broken
			}
			continue
		}
		sites[id] = s
	}
	hosts := map[string]*site{}
	for _, s := range sites {
		for _, h := range s.cfg.Hosts {
			hosts[strings.ToLower(h)] = s
		}
	}
	sv.mu.Lock()
	sv.sites, sv.hosts = sites, hosts
	sv.mu.Unlock()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Watch loads the settings every few seconds until ctx ends.
func (sv *Server) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := sv.Load(); err != nil {
				log.Printf("waf: %v", err)
			}
		}
	}
}

// clientIP is the visitor's address as the edge saw it (the edge sets X-Forwarded-For and drops a visitor's own).
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		first, _, _ := strings.Cut(v, ",")
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip.String()
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ServeHTTP sends a visit through the WAF of the website its host names.
func (sv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	sv.mu.RLock()
	s := sv.hosts[host]
	sv.mu.RUnlock()
	if s == nil {
		http.Error(w, "unknown website", http.StatusMisdirectedRequest)
		return
	}
	r.RemoteAddr = net.JoinHostPort(clientIP(r), "0")
	s.h.ServeHTTP(w, r)
}

// Sites lists the websites served now (for the health answer).
func (sv *Server) Sites() []string {
	sv.mu.RLock()
	defer sv.mu.RUnlock()
	out := make([]string, 0, len(sv.sites))
	for id := range sv.sites {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
