package sites

// Domains after create (DOM-01): add or remove extra names of a website, route names whose DNS now points here,
// redirects per path, and a DNS and certificate check per name. Every change is a job: the edge configuration is
// rewritten (Docker labels or the native edge entry), and when the website's main address moves (from the preview
// name to its domain), WordPress gets its addresses rewritten.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const MaxRedirects = 50

// Redirect sends visitors of one path of the website elsewhere. It applies on the website's main address (every
// other name of the website redirects there first).
type Redirect struct {
	From string `json:"from"` // a path on the website, e.g. /old-page
	To   string `json:"to"`   // an http(s) address, or a path on the website
	Code int    `json:"code"` // 301 (permanent) | 302 (temporary)
}

var (
	redirFromRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)
	redirToRe   = regexp.MustCompile(`^(https?://[a-z0-9.-]+(:[0-9]{1,5})?)?/[A-Za-z0-9._~/?#&=%+:,;@-]{0,500}$`)
)

// checkRedirect validates one redirect; every character that could leave a label or a YAML string is refused.
func checkRedirect(r Redirect) error {
	if !redirFromRe.MatchString(r.From) || strings.Contains(r.From, "//") || strings.Contains(r.From, "/../") || strings.HasSuffix(r.From, "/..") {
		return fmt.Errorf("%w: the path to redirect must start with / and use letters, digits, . _ ~ - and /", ErrBadInput)
	}
	if strings.HasPrefix(r.From, "/.well-known/") || r.From == "/.well-known" {
		return fmt.Errorf("%w: /.well-known is needed for certificates and cannot be redirected", ErrBadInput)
	}
	to := r.To
	if !strings.HasPrefix(to, "/") {
		i := strings.Index(to, "://")
		if i < 0 {
			return fmt.Errorf("%w: the target must be an http(s) address or a path starting with /", ErrBadInput)
		}
		if !strings.Contains(to[i+3:], "/") {
			to += "/" // https://example.com is https://example.com/
		}
	}
	if !redirToRe.MatchString(to) {
		return fmt.Errorf("%w: the target must be an http(s) address or a path starting with /, without spaces, quotes, $ or braces", ErrBadInput)
	}
	if h, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(to, "https://"), "http://"), "/"); ok && !strings.HasPrefix(to, "/") {
		host, _, _ := strings.Cut(h, ":")
		if spec.Domain(host) != nil {
			return fmt.Errorf("%w: the target's host name is not valid", ErrBadInput)
		}
	}
	if strings.TrimRight(to, "/") == strings.TrimRight(r.From, "/") {
		return fmt.Errorf("%w: a path cannot redirect to itself", ErrBadInput)
	}
	if r.Code != 301 && r.Code != 302 {
		return fmt.Errorf("%w: a redirect is 301 (permanent) or 302 (temporary)", ErrBadInput)
	}
	return nil
}

// redirectRegex matches one path (with or without a closing slash, any query) on any host; no backslash, so it
// sits as is in a compose label and in a YAML string. end is the end-of-line anchor ("$$" in compose).
func redirectRegex(from, end string) string {
	var b strings.Builder
	b.WriteString("^https?://[^/]+")
	p := strings.TrimRight(from, "/")
	for _, c := range p {
		if c == '.' {
			b.WriteString("[.]")
		} else {
			b.WriteRune(c)
		}
	}
	b.WriteString("/?([?].*)?" + end)
	return b.String()
}

// redirectTarget is where a redirect goes; a path stays on the main address, and the visit's query follows unless
// the target has its own. dollar is "$$" in compose.
func (s Site) redirectTarget(r Redirect, dollar string) string {
	t := r.To
	if strings.HasPrefix(t, "/") {
		t = "https://" + s.Canonical() + t
	}
	if !strings.ContainsAny(t, "?#") {
		t += dollar + "{1}"
	}
	return t
}

// redirectMiddlewares names the website's redirect middlewares in order.
func (s Site) redirectMiddlewares() []string {
	out := make([]string, len(s.Redirects))
	for i := range s.Redirects {
		out[i] = fmt.Sprintf("site-%s-r%d", s.ID, i)
	}
	return out
}

// DNS verdicts of one name.
const (
	DNSHere      = "here"      // every IPv4 answer is this server
	DNSElsewhere = "elsewhere" // it points to another server
	DNSMixed     = "mixed"     // some answers are this server, some are not
	DNSNone      = "none"      // no address record (or the name does not exist)
	DNSError     = "error"     // the lookup failed
)

// DomainCheck is one name of a website with what DNS and the edge say about it now.
type DomainCheck struct {
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Routed bool     `json:"routed"`
	DNS    string   `json:"dns"`
	IPv4   []string `json:"ipv4,omitempty"`
	IPv6   []string `json:"ipv6,omitempty"`
	Error  string   `json:"error,omitempty"`
	// Cert: valid | pending (the edge has no certificate for the name yet) | error; empty when not routed
	Cert        string `json:"cert,omitempty"`
	CertIssuer  string `json:"cert_issuer,omitempty"`
	CertExpires int64  `json:"cert_expires,omitempty"`
	CertError   string `json:"cert_error,omitempty"`
}

// Domains is the Domains page of a website.
type Domains struct {
	PublicIPv4  string        `json:"public_ipv4"`
	PreviewZone string        `json:"preview_zone,omitempty"`
	Canonical   string        `json:"canonical"`
	Hosts       []DomainCheck `json:"hosts"`
	Redirects   []Redirect    `json:"redirects"`
	MaxAliases  int           `json:"max_aliases"`
	Job         *JobView      `json:"job,omitempty"` // the newest domains job of the website
}

// lookup classifies what DNS says about a name.
func (m *Manager) lookup(ctx context.Context, host string) DomainCheck {
	d := DomainCheck{Name: host}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := m.resolve(ctx, host)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			d.DNS = DNSNone
			return d
		}
		d.DNS, d.Error = DNSError, err.Error()
		return d
	}
	here, other := 0, 0
	for _, a := range addrs {
		ip := net.ParseIP(a)
		switch {
		case ip == nil:
		case ip.To4() != nil:
			d.IPv4 = append(d.IPv4, a)
			if a == m.cfg.PublicIPv4 {
				here++
			} else {
				other++
			}
		default:
			d.IPv6 = append(d.IPv6, a)
		}
	}
	sort.Strings(d.IPv4)
	sort.Strings(d.IPv6)
	switch {
	case here > 0 && other == 0 && m.cfg.PublicIPv4 != "":
		d.DNS = DNSHere
	case here > 0:
		d.DNS = DNSMixed
	case other > 0:
		d.DNS = DNSElsewhere
	default:
		d.DNS = DNSNone
	}
	return d
}

// Domains checks every name of a website: DNS for each, and the edge's certificate for every routed one.
func (m *Manager) Domains(ctx context.Context, id string) (Domains, error) {
	s, err := m.Get(id)
	if err != nil {
		return Domains{}, err
	}
	out := Domains{PublicIPv4: m.cfg.PublicIPv4, PreviewZone: m.cfg.PreviewZone, Canonical: s.Canonical(),
		Redirects: s.Redirects, MaxAliases: spec.MaxAliases}
	if out.Redirects == nil {
		out.Redirects = []Redirect{}
	}
	out.Hosts = make([]DomainCheck, len(s.Hosts))
	var wg sync.WaitGroup
	for i, h := range s.Hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := m.lookup(ctx, h.Name)
			d.Role, d.Routed = h.Role, h.Routed
			if h.Routed {
				t := m.tlsCheck(ctx, h.Name)
				d.CertIssuer, d.CertExpires = t.Issuer, t.Expires
				switch {
				case t.OK:
					d.Cert = "valid"
				case strings.Contains(strings.ToUpper(t.Issuer), "TRAEFIK DEFAULT"):
					d.Cert = "pending"
				default:
					d.Cert, d.CertError = "error", t.Error
				}
			}
			out.Hosts[i] = d
		}()
	}
	wg.Wait()
	for _, j := range m.jobs.List() {
		if j.Kind == "domains" && j.Site == id && (out.Job == nil || j.Started.After(out.Job.Started)) {
			jv := j
			out.Job = &jv
		}
	}
	return out, nil
}

// DomainChange is one change on the Domains page.
type DomainChange struct {
	Op   string `json:"op"` // add | remove | refresh | redirect-add | redirect-remove
	Name string `json:"name,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Code int    `json:"code,omitempty"`
}

// ErrNoChange is returned when a change leaves the website as it is (for example no new name points here yet).
var ErrNoChange = errors.New("nothing to change")

// nextDomains applies a change to a copy of the website.
func (m *Manager) nextDomains(ctx context.Context, s Site, c DomainChange) (Site, error) {
	next := s
	next.Hosts = append([]Host(nil), s.Hosts...)
	next.Redirects = append([]Redirect(nil), s.Redirects...)
	switch c.Op {
	case "add":
		name := strings.ToLower(strings.TrimSpace(c.Name))
		if err := spec.Domain(name); err != nil {
			return s, fmt.Errorf("%w: %v", ErrBadInput, err)
		}
		if z := m.cfg.PreviewZone; z != "" && (name == z || strings.HasSuffix(name, "."+z)) {
			return s, fmt.Errorf("%w: names under %s are the panel's preview addresses", ErrBadInput, z)
		}
		for _, h := range s.Hosts {
			if h.Name == name {
				return s, fmt.Errorf("%w: %s is already a name of this website", ErrBadInput, name)
			}
		}
		res, err := m.reserved()
		if err != nil {
			return s, err
		}
		if res[name] {
			return s, fmt.Errorf("%w: %s is already in use on this server", ErrBadInput, name)
		}
		n := 0
		for _, h := range s.Hosts {
			if h.Role == "alias" {
				n++
			}
		}
		if n >= spec.MaxAliases {
			return s, fmt.Errorf("%w: a website has at most %d extra names", ErrBadInput, spec.MaxAliases)
		}
		next.Hosts = append(next.Hosts, Host{Name: name, Role: "alias", Routed: m.pointsHere(ctx, name)})
	case "remove":
		i := -1
		for k, h := range s.Hosts {
			if h.Name == c.Name {
				i = k
			}
		}
		if i < 0 {
			return s, fmt.Errorf("%w: %s is not a name of this website", ErrBadInput, c.Name)
		}
		if r := s.Hosts[i].Role; r != "alias" && r != "www" {
			return s, fmt.Errorf("%w: the main domain and the preview address stay", ErrBadInput)
		}
		next.Hosts = append(next.Hosts[:i:i], next.Hosts[i+1:]...)
		if next.Canonical() == "" {
			return s, fmt.Errorf("%w: the website needs at least one name that is served", ErrBadInput)
		}
	case "refresh":
		for i, h := range next.Hosts {
			if !h.Routed && m.pointsHere(ctx, h.Name) {
				next.Hosts[i].Routed = true
			}
		}
	case "redirect-add":
		r := Redirect{From: strings.TrimSpace(c.From), To: strings.TrimSpace(c.To), Code: c.Code}
		if err := checkRedirect(r); err != nil {
			return s, err
		}
		if len(s.Redirects) >= MaxRedirects {
			return s, fmt.Errorf("%w: a website has at most %d redirects", ErrBadInput, MaxRedirects)
		}
		for _, x := range s.Redirects {
			if strings.TrimRight(x.From, "/") == strings.TrimRight(r.From, "/") {
				return s, fmt.Errorf("%w: %s is already redirected", ErrBadInput, r.From)
			}
		}
		next.Redirects = append(next.Redirects, r)
	case "redirect-remove":
		i := -1
		for k, x := range s.Redirects {
			if x.From == c.From {
				i = k
			}
		}
		if i < 0 {
			return s, fmt.Errorf("%w: %s is not redirected", ErrBadInput, c.From)
		}
		next.Redirects = append(next.Redirects[:i:i], next.Redirects[i+1:]...)
	default:
		return s, fmt.Errorf("%w: unknown change", ErrBadInput)
	}
	if len(next.Redirects) == 0 {
		next.Redirects = nil
	}
	if same(s.Hosts, next.Hosts) && sameRedirects(s.Redirects, next.Redirects) {
		return s, ErrNoChange
	}
	next.URL = "https://" + next.Canonical()
	return next, nil
}

func same(a, b []Host) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameRedirects(a, b []Redirect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ChangeDomains checks a change and starts the job that applies it.
func (m *Manager) ChangeDomains(ctx context.Context, id string, c DomainChange) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	next, err := m.nextDomains(ctx, s, c)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	j := m.jobs.New("domains", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := m.applyDomains(ctx, j, s, next); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

// applyDomains writes the website's routes and, when its main address moved, rewrites WordPress's addresses. On a
// failure the previous routes come back.
func (m *Manager) applyDomains(ctx context.Context, j *Job, prev, next Site) error {
	j.Step("routes")
	if err := m.writeRoutes(ctx, next); err != nil {
		return err
	}
	if prev.Canonical() == next.Canonical() || !next.IsWP() {
		return nil
	}
	j.Step("addresses")
	undo := func(cause error) error {
		if err := m.writeRoutes(ctx, prev); err != nil {
			return fmt.Errorf("%w; putting the previous names back failed too: %v", cause, err)
		}
		return fmt.Errorf("%w; the website keeps its previous address", cause)
	}
	r, err := m.wp(ctx, next.ID, nil, []string{"wp", "option", "get", "home"})
	if err != nil || r[0].RC != 0 {
		return undo(fmt.Errorf("WordPress did not answer: %s", wpText(r, err)))
	}
	home := strings.TrimSpace(r[0].Out)
	cmds, err := replaceCmds(home, next.URL)
	if err != nil {
		return undo(err)
	}
	res, err := m.wp(ctx, next.ID, nil, cmds...)
	failed := err
	if err == nil {
		for i, x := range res {
			if x.RC != 0 {
				failed = fmt.Errorf("rewriting the addresses failed (%s): %s", strings.Join(cmds[i][1:3], " "), tail([]byte(x.Err+x.Out)))
				break
			}
		}
	}
	if failed != nil {
		if back, err := replaceCmds(next.URL, home); err == nil {
			_, _ = m.wp(ctx, next.ID, nil, back...)
		}
		return undo(failed)
	}
	_, _ = m.wp(ctx, next.ID, nil, []string{"wp", "cache", "flush"})
	j.Step("check")
	r, err = m.wp(ctx, next.ID, nil, []string{"wp", "option", "get", "home"})
	if err != nil || r[0].RC != 0 || strings.TrimRight(strings.TrimSpace(r[0].Out), "/") != strings.TrimRight(next.URL, "/") {
		return fmt.Errorf("the check failed: WordPress's address is not %s: %s", next.URL, wpText(r, err))
	}
	return nil
}

// writeRoutes writes a website's names and redirects into its edge configuration and site.json.
func (m *Manager) writeRoutes(ctx context.Context, s Site) error {
	if !s.IsNative() {
		return m.writeLimits(ctx, s) // compose.yml (labels) and site.json; the containers follow with up -d
	}
	if m.native == nil {
		return errNativeOff
	}
	p := m.npaths
	files := []string{p.nginx(s.ID), p.edge(s.ID), filepath.Join(m.dir(s.ID), "site.json")}
	prev := map[string][]byte{}
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			prev[f] = b
		}
	}
	putBack := func() {
		for _, f := range files {
			if b, ok := prev[f]; ok {
				_ = writeFile(f, b, 0o644)
			} else {
				_ = os.Remove(f)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	err := writeFile(p.nginx(s.ID), []byte(s.NativeNginx(p, m.native.Gateway)), 0o644)
	if err == nil {
		err = m.writeNativeEdge(s)
	}
	if err == nil {
		err = writeSite(m.dir(s.ID), s)
	}
	if err != nil {
		putBack()
		return err
	}
	if out, err := m.nginxReload(ctx); err != nil {
		putBack()
		_, _ = m.nginxReload(ctx)
		return fmt.Errorf("the web server refused the new names, the previous ones are back: %s", tail(out))
	}
	return nil
}
