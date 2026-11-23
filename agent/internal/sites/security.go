package sites

// Website security at the edge (SEC-01 wave 1), like cPanel's IP Blocker, Directory Privacy and Hotlink Protection.
// Every rule is a Traefik router in front of the website's own routers, so it works the same for Docker and native
// websites and for every app: blocked addresses and hotlinked files get 403 from the edge, protected folders ask for
// a login (basic auth). Passwords never reach the agent: the panel sends bcrypt hashes, which are kept only in the
// edge's password files (not in site.json).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const (
	MaxBlocked      = 200
	MaxProtected    = 20
	MaxAuthUsers    = 20
	MaxHotlinkAllow = 20
	edgeUID         = 65532 // the edge (Traefik) runs as this user
	// priorities: above the website's own routers (Traefik's default priority is the rule's length)
	prioBlock   = 30000
	prioHotlink = 20000
	prioAuth    = 10000
)

// HotlinkExts are the files hotlink protection covers.
var HotlinkExts = []string{"jpg", "jpeg", "png", "gif", "webp", "avif", "svg", "bmp", "ico", "mp4", "webm", "mp3"}

// Protected is a folder that asks for a login. Users holds the names only; the hashes live in the edge's file.
type Protected struct {
	Path  string   `json:"path"`
	Users []string `json:"users"`
	Rev   int      `json:"rev,omitempty"` // goes up with every password change, so the edge reads the file again
}

// Hotlink protection: other sites may not embed the website's images and media; Allow lists extra hosts that may.
type Hotlink struct {
	Allow []string `json:"allow"`
}

var (
	authUserRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	bcryptRe   = regexp.MustCompile(`^\$2[aby]\$(0[4-9]|[12][0-9]|3[01])\$[./A-Za-z0-9]{53}$`)
)

// checkBlock parses an address or network to block; networks wider than /8 (IPv4) or /16 (IPv6) are refused.
func checkBlock(v string) (string, error) {
	v = strings.TrimSpace(v)
	if a, err := netip.ParseAddr(v); err == nil && !a.Is4In6() && a.Zone() == "" {
		return a.String(), nil
	}
	p, err := netip.ParsePrefix(v)
	if err != nil || p.Addr().Zone() != "" {
		return "", fmt.Errorf("%w: %q is not an IP address or network (for example 203.0.113.7 or 203.0.113.0/24)", ErrBadInput, v)
	}
	if (p.Addr().Is4() && p.Bits() < 8) || (!p.Addr().Is4() && p.Bits() < 16) {
		return "", fmt.Errorf("%w: %s is too wide to block", ErrBadInput, v)
	}
	return p.Masked().String(), nil
}

func checkProtectPath(p string) error {
	if !redirFromRe.MatchString(p) || strings.Contains(p, "//") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
		return fmt.Errorf("%w: the folder must start with / and use letters, digits, . _ ~ - and /", ErrBadInput)
	}
	if p == "/.well-known" || strings.HasPrefix(p, "/.well-known/") {
		return fmt.Errorf("%w: /.well-known is needed for certificates and cannot be protected", ErrBadInput)
	}
	return nil
}

// cleanFolder is a protected folder without a closing slash ("/" stays "/").
func cleanFolder(p string) string {
	if p == "/" {
		return p
	}
	return strings.TrimRight(p, "/")
}

// authFile is the edge's password file of one protected folder, on the host and as the edge sees it.
func authName(id, path string) string {
	h := sha256.Sum256([]byte(path))
	return "site-" + id + "-" + hex.EncodeToString(h[:6]) + ".htpasswd"
}

// authFile is the host path of a protected folder's password file.
func (m *Manager) authFile(id, path string) string {
	return filepath.Join(m.cfg.Data, "edge", "dynamic", "auth", authName(id, path))
}

// edgeAuthFile is the path the edge reads for a protected folder.
func (s Site) edgeAuthFile(path string) string { return "/dynamic/auth/" + authName(s.ID, path) }

// readAuth reads a password file into name -> hash.
func readAuth(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if n, h, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && authUserRe.MatchString(n) && bcryptRe.MatchString(h) {
			out[n] = h
		}
	}
	return out
}

// writeAuth writes a password file the edge (uid 65532) can read and nobody else.
func writeAuth(path string, users map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(users))
	for n := range users {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + ":" + users[n] + "\n")
	}
	if err := writeFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chown(path, edgeUID, edgeUID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return nil
}

// SecurityView is the Security page of a website.
type SecurityView struct {
	Blocked     []string    `json:"blocked"`
	Protected   []Protected `json:"protected"`
	Hotlink     *Hotlink    `json:"hotlink"`
	HotlinkExts []string    `json:"hotlink_exts"`
	// wave 2: only for websites with their own web server
	WebServer  bool     `json:"web_server"`
	ErrorPages []int    `json:"error_pages"`
	ErrorCodes []int    `json:"error_codes"`
	ErrorDir   string   `json:"error_dir"`
	MIME       []MIME   `json:"mime"`
	Job        *JobView `json:"job,omitempty"`
	// wave 3
	Logins LoginsView `json:"logins"`
	Scan   ScanView   `json:"scan"`
	WAF    WAFView    `json:"waf"`
	// WAFReady is true when the firewall service runs (the panel update brings it)
	WAFReady bool `json:"waf_ready"`
	// SITE-03
	Headers       []HeaderRule `json:"headers"`
	HeaderPresets []string     `json:"header_presets"`
	Allowed       []string     `json:"allowed"`
}

// Security returns a website's edge rules.
func (m *Manager) Security(id string) (SecurityView, error) {
	s, err := m.Get(id)
	if err != nil {
		return SecurityView{}, err
	}
	v := SecurityView{Blocked: s.BlockIPs, Protected: s.Protected, Hotlink: s.Hotlink, HotlinkExts: HotlinkExts,
		WebServer: s.HasWebServer(), ErrorPages: s.ErrorPages, ErrorCodes: ErrorCodes, ErrorDir: s.ErrorDir(), MIME: s.MIME}
	if v.ErrorPages == nil {
		v.ErrorPages = []int{}
	}
	if v.MIME == nil {
		v.MIME = []MIME{}
	}
	if v.Blocked == nil {
		v.Blocked = []string{}
	}
	v.Headers, v.Allowed, v.HeaderPresets = s.Headers, s.AllowIPs, HeaderPresets
	if v.Headers == nil {
		v.Headers = []HeaderRule{}
	}
	if v.Allowed == nil {
		v.Allowed = []string{}
	}
	if v.Protected == nil {
		v.Protected = []Protected{}
	}
	v.Logins = m.Logins(s)
	v.WAF = m.wafView(s)
	v.WAFReady = m.wafReady(context.Background())
	if v.Scan, err = m.Scan(id); err != nil {
		return SecurityView{}, err
	}
	for _, j := range m.jobs.List() {
		if j.Kind == "security" && j.Site == id && (v.Job == nil || j.Started.After(v.Job.Started)) {
			jv := j
			v.Job = &jv
		}
	}
	return v, nil
}

// SecurityChange is one change on the Security page.
type SecurityChange struct {
	Op    string   `json:"op"` // block-add | block-remove | protect-set | protect-user-remove | protect-remove | hotlink-on | hotlink-off | errpage-on | errpage-off | mime-add | mime-remove | waf-* | optimize-set
	IP    string   `json:"ip,omitempty"`
	Path  string   `json:"path,omitempty"`
	User  string   `json:"user,omitempty"`
	Hash  string   `json:"hash,omitempty"` // bcrypt, made by the panel
	Allow []string `json:"allow,omitempty"`
	Code  int      `json:"code,omitempty"`  // errpage-on | errpage-off
	Type  string   `json:"type,omitempty"`  // mime-add | mime-remove
	Exts  string   `json:"exts,omitempty"`  // mime-add: "tex ltx"
	Mode  string   `json:"mode,omitempty"`  // waf-set: detect | block
	Level int      `json:"level,omitempty"` // waf-set: paranoia level 1 or 2
	Rule  int      `json:"rule,omitempty"`  // waf-exclude-add | waf-exclude-remove
	// optimize-set (PHP-01): compression at the edge and browser cache days for static files (0 = off)
	Compress bool `json:"compress,omitempty"`
	Days     int  `json:"days,omitempty"`
	// header-set | header-remove (SITE-03): Path, Name and Value; allow-add | allow-remove: IP
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// nextSecurity applies a change to a copy of the website; auth holds the password file it needs (path -> users).
func (m *Manager) nextSecurity(s Site, c SecurityChange) (Site, map[string]map[string]string, error) {
	next := s
	next.BlockIPs = append([]string(nil), s.BlockIPs...)
	next.Protected = nil
	for _, p := range s.Protected {
		next.Protected = append(next.Protected, Protected{Path: p.Path, Users: append([]string(nil), p.Users...), Rev: p.Rev})
	}
	auth := map[string]map[string]string{}
	find := func(path string) int {
		for i, p := range next.Protected {
			if p.Path == path {
				return i
			}
		}
		return -1
	}
	if ok, err := nextWebRules(s, &next, c); ok {
		return next, auth, err
	}
	if ok, err := m.nextWAF(s, &next, c); ok {
		return next, auth, err
	}
	if ok, err := nextOptimize(s, &next, c); ok {
		return next, auth, err
	}
	if ok, err := m.nextHeaders(s, &next, c); ok {
		return next, auth, err
	}
	switch c.Op {
	case "block-add":
		v, err := checkBlock(c.IP)
		if err != nil {
			return s, nil, err
		}
		if a, err := netip.ParseAddr(m.cfg.PublicIPv4); err == nil {
			if p, err := netip.ParsePrefix(v); (err == nil && p.Contains(a)) || v == a.String() {
				return s, nil, fmt.Errorf("%w: %s holds this server's own address", ErrBadInput, v)
			}
		}
		for _, x := range s.BlockIPs {
			if x == v {
				return s, nil, fmt.Errorf("%w: %s is already blocked", ErrBadInput, v)
			}
		}
		if len(s.BlockIPs) >= MaxBlocked {
			return s, nil, fmt.Errorf("%w: at most %d blocked addresses", ErrBadInput, MaxBlocked)
		}
		next.BlockIPs = append(next.BlockIPs, v)
	case "block-remove":
		i := -1
		for k, x := range s.BlockIPs {
			if x == c.IP {
				i = k
			}
		}
		if i < 0 {
			return s, nil, fmt.Errorf("%w: %s is not blocked", ErrBadInput, c.IP)
		}
		next.BlockIPs = append(next.BlockIPs[:i:i], next.BlockIPs[i+1:]...)
	case "protect-set":
		path := cleanFolder(strings.TrimSpace(c.Path))
		if err := checkProtectPath(path); err != nil {
			return s, nil, err
		}
		if !authUserRe.MatchString(c.User) {
			return s, nil, fmt.Errorf("%w: a user name has 1-32 letters, digits, . _ or -", ErrBadInput)
		}
		if !bcryptRe.MatchString(c.Hash) {
			return s, nil, fmt.Errorf("%w: the password hash is not bcrypt", ErrBadInput)
		}
		i := find(path)
		if i < 0 {
			if len(next.Protected) >= MaxProtected {
				return s, nil, fmt.Errorf("%w: at most %d protected folders", ErrBadInput, MaxProtected)
			}
			next.Protected = append(next.Protected, Protected{Path: path})
			i = len(next.Protected) - 1
		}
		users := map[string]string{}
		if len(next.Protected[i].Users) > 0 {
			users = readAuth(m.authFile(s.ID, path))
		}
		if _, ok := users[c.User]; !ok && len(users) >= MaxAuthUsers {
			return s, nil, fmt.Errorf("%w: at most %d users per folder", ErrBadInput, MaxAuthUsers)
		}
		users[c.User] = c.Hash
		auth[path] = users
		next.Protected[i].Users = sortedKeys(users)
		next.Protected[i].Rev++
	case "protect-user-remove":
		i := find(c.Path)
		if i < 0 {
			return s, nil, fmt.Errorf("%w: %s is not protected", ErrBadInput, c.Path)
		}
		host := m.authFile(s.ID, c.Path)
		users := readAuth(host)
		if _, ok := users[c.User]; !ok {
			return s, nil, fmt.Errorf("%w: %s has no user %s", ErrBadInput, c.Path, c.User)
		}
		if len(users) == 1 {
			return s, nil, fmt.Errorf("%w: the last user cannot be removed; remove the protection instead", ErrBadInput)
		}
		delete(users, c.User)
		auth[c.Path] = users
		next.Protected[i].Users = sortedKeys(users)
		next.Protected[i].Rev++
	case "protect-remove":
		i := find(c.Path)
		if i < 0 {
			return s, nil, fmt.Errorf("%w: %s is not protected", ErrBadInput, c.Path)
		}
		next.Protected = append(next.Protected[:i:i], next.Protected[i+1:]...)
		auth[c.Path] = nil
	case "hotlink-on":
		var allow []string
		seen := map[string]bool{}
		for _, a := range c.Allow {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" || seen[a] {
				continue
			}
			if err := spec.Domain(a); err != nil {
				return s, nil, fmt.Errorf("%w: allowed site %q: %v", ErrBadInput, a, err)
			}
			seen[a] = true
			allow = append(allow, a)
		}
		if len(allow) > MaxHotlinkAllow {
			return s, nil, fmt.Errorf("%w: at most %d allowed sites", ErrBadInput, MaxHotlinkAllow)
		}
		if allow == nil {
			allow = []string{}
		}
		next.Hotlink = &Hotlink{Allow: allow}
	case "hotlink-off":
		if s.Hotlink == nil {
			return s, nil, ErrNoChange
		}
		next.Hotlink = nil
	default:
		return s, nil, fmt.Errorf("%w: unknown change", ErrBadInput)
	}
	if len(next.BlockIPs) == 0 {
		next.BlockIPs = nil
	}
	if len(next.Protected) == 0 {
		next.Protected = nil
	}
	return next, auth, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ChangeSecurity checks a change and starts the job that applies it: password files first, then the routes. On a
// failure the previous password files and routes come back.
func (m *Manager) ChangeSecurity(id string, c SecurityChange) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	next, auth, err := m.nextSecurity(s, c)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	kind := "security"
	if c.Op == "optimize-set" || strings.HasPrefix(c.Op, "header-") {
		kind = "optimize" // shown on the Performance page
	}
	j := m.jobs.New(kind, id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		j.Step("routes")
		if c.Op == "errpage-on" {
			if err := m.writeStarterPage(next, c.Code); err != nil {
				j.Fail("writing the starter page failed: " + err.Error())
				return
			}
		}
		prev := map[string][]byte{}
		for path, users := range auth {
			host := m.authFile(id, path)
			if b, err := os.ReadFile(host); err == nil {
				prev[host] = b
			}
			if users == nil {
				continue // removed after the routes no longer use it
			}
			if err := writeAuth(host, users); err != nil {
				j.Fail(err.Error())
				return
			}
		}
		if err := m.writeRoutes(ctx, next); err != nil {
			for path := range auth {
				host := m.authFile(id, path)
				if b, ok := prev[host]; ok {
					_ = writeFile(host, b, 0o600)
				} else {
					_ = os.Remove(host)
				}
			}
			j.Fail(err.Error())
			return
		}
		for path, users := range auth {
			if users == nil {
				host := m.authFile(id, path)
				_ = os.Remove(host)
			}
		}
		j.Done("")
	}()
	return j.ID, nil
}

// edgeRouter is one security router in front of a website.
type edgeRouter struct {
	name, rule  string
	priority    int
	middlewares []string
}

// edgeMiddleware is one middleware of the security routers: kind deny (403 for everyone) or auth (basic auth).
type edgeMiddleware struct {
	name, kind, file string
	value            string      // compress: the content types; cache: the Cache-Control value
	hdrs             [][2]string // headers: name and value ("" removes it)
}

func hostRule(hosts []string) string {
	parts := make([]string, len(hosts))
	for i, h := range hosts {
		parts[i] = "Host(`" + h + "`)"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " || ") + ")"
}

func regexQuote(s string) string { return strings.ReplaceAll(s, ".", "[.]") }

// securityEdge lists the website's security routers and middlewares. dollar is "$$" in compose labels.
func (s Site) securityEdge(dollar string) ([]edgeRouter, []edgeMiddleware) {
	canon := s.Canonical()
	var routed []string
	for _, h := range s.Hosts {
		if h.Routed {
			routed = append(routed, h.Name)
		}
	}
	if canon == "" || len(routed) == 0 {
		return nil, nil
	}
	rid := "site-" + s.ID
	deny := rid + "-deny"
	var rs []edgeRouter
	var ms []edgeMiddleware
	if len(s.BlockIPs) > 0 {
		ips := make([]string, len(s.BlockIPs))
		for i, ip := range s.BlockIPs {
			ips[i] = "ClientIP(`" + ip + "`)"
		}
		rs = append(rs, edgeRouter{name: rid + "-block", rule: hostRule(routed) + " && (" + strings.Join(ips, " || ") + ")", priority: prioBlock, middlewares: []string{deny}})
	}
	rs = append(rs, s.allowRouter(rid, routed, deny)...)
	if s.Hotlink != nil {
		allowed := []string{}
		for _, h := range routed {
			allowed = append(allowed, regexQuote(h))
		}
		for _, h := range s.Hotlink.Allow {
			allowed = append(allowed, regexQuote(h))
		}
		rule := hostRule([]string{canon}) +
			" && PathRegexp(`(?i)[.](" + strings.Join(HotlinkExts, "|") + ")" + dollar + "`)" +
			" && HeaderRegexp(`Referer`, `.+`)" +
			" && !HeaderRegexp(`Referer`, `^https?://(" + strings.Join(allowed, "|") + ")(:[0-9]+)?(/|" + dollar + ")`)"
		rs = append(rs, edgeRouter{name: rid + "-hotlink", rule: rule, priority: prioHotlink, middlewares: []string{deny}})
	}
	for i, p := range s.Protected {
		mw := fmt.Sprintf("%s-auth%d-r%d", rid, i, p.Rev)
		rule := hostRule([]string{canon})
		if p.Path != "/" {
			rule += " && (Path(`" + p.Path + "`) || PathPrefix(`" + p.Path + "/`))"
		}
		chain := s.chainWith(mw)
		rs = append(rs, edgeRouter{name: fmt.Sprintf("%s-protect%d", rid, i), rule: rule, priority: prioAuth + len(p.Path), middlewares: chain})
		ms = append(ms, edgeMiddleware{name: mw, kind: "auth", file: s.edgeAuthFile(p.Path)})
	}
	if len(s.BlockIPs) > 0 || len(s.AllowIPs) > 0 || s.Hotlink != nil {
		ms = append([]edgeMiddleware{{name: deny, kind: "deny"}}, ms...)
	}
	ors, oms := s.optimizeEdge(canon, dollar)
	hrs, hms := s.headerEdge(canon, dollar)
	return append(append(rs, hrs...), ors...), append(append(ms, oms...), hms...)
}

// securityLabels renders the security routers as Traefik labels.
func (s Site) securityLabels() []string {
	rs, ms := s.securityEdge("$$")
	var l []string
	for _, m := range ms {
		switch m.kind {
		case "deny":
			l = append(l, fmt.Sprintf(`traefik.http.middlewares.%s.ipallowlist.sourcerange: "127.0.0.2/32"`, m.name))
		case "auth":
			l = append(l,
				fmt.Sprintf(`traefik.http.middlewares.%s.basicauth.usersfile: "%s"`, m.name, m.file),
				fmt.Sprintf(`traefik.http.middlewares.%s.basicauth.realm: "Protected"`, m.name),
				fmt.Sprintf(`traefik.http.middlewares.%s.basicauth.removeheader: "true"`, m.name))
		case "compress":
			l = append(l, fmt.Sprintf(`traefik.http.middlewares.%s.compress.includedcontenttypes: "%s"`, m.name, m.value))
		case "cache":
			l = append(l, fmt.Sprintf(`traefik.http.middlewares.%s.headers.customresponseheaders.Cache-Control: "%s"`, m.name, m.value))
		case "headers":
			for _, h := range m.hdrs {
				l = append(l, fmt.Sprintf(`traefik.http.middlewares.%s.headers.customresponseheaders.%s: "%s"`, m.name, h[0], h[1]))
			}
		}
	}
	for _, r := range rs {
		l = append(l,
			fmt.Sprintf("traefik.http.routers.%s.rule: %q", r.name, r.rule),
			fmt.Sprintf(`traefik.http.routers.%s.priority: "%d"`, r.name, r.priority),
			fmt.Sprintf(`traefik.http.routers.%s.entrypoints: websecure`, r.name),
			fmt.Sprintf(`traefik.http.routers.%s.tls.certresolver: le`, r.name),
			fmt.Sprintf(`traefik.http.routers.%s.service: %s`, r.name, s.wafService(true)),
			fmt.Sprintf(`traefik.http.routers.%s.middlewares: %s`, r.name, strings.Join(r.middlewares, ",")))
	}
	return l
}

// securityEdgeYAML renders the security routers and middlewares for the native edge file (indented as entries of
// routers and of middlewares).
func (s Site) securityEdgeYAML() (routers, middlewares string) {
	rs, ms := s.securityEdge("$")
	var r, m strings.Builder
	for _, x := range rs {
		fmt.Fprintf(&r, "    %s:\n      rule: %q\n      priority: %d\n      entryPoints: [websecure]\n      service: %s\n      tls:\n        certResolver: le\n      middlewares: [%s]\n",
			x.name, x.rule, x.priority, s.wafService(false), strings.Join(x.middlewares, ", "))
	}
	for _, x := range ms {
		switch x.kind {
		case "deny":
			fmt.Fprintf(&m, "    %s:\n      ipAllowList:\n        sourceRange: [\"127.0.0.2/32\"]\n", x.name)
		case "auth":
			fmt.Fprintf(&m, "    %s:\n      basicAuth:\n        usersFile: %q\n        realm: \"Protected\"\n        removeHeader: true\n", x.name, x.file)
		case "compress":
			fmt.Fprintf(&m, "    %s:\n      compress:\n        includedContentTypes: [%s]\n", x.name, strings.ReplaceAll(x.value, ",", ", "))
		case "cache":
			fmt.Fprintf(&m, "    %s:\n      headers:\n        customResponseHeaders:\n          Cache-Control: %q\n", x.name, x.value)
		case "headers":
			fmt.Fprintf(&m, "    %s:\n      headers:\n        customResponseHeaders:\n", x.name)
			for _, h := range x.hdrs {
				fmt.Fprintf(&m, "          %s: %q\n", h[0], h[1])
			}
		}
	}
	return r.String(), m.String()
}
