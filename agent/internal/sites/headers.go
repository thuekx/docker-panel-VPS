package sites

// Response headers and the address allow-list (SITE-03), at the edge for every app and both engines.
//
// Headers: a website sets its own response headers (Content-Security-Policy, Strict-Transport-Security,
// Cache-Control …) for all its answers ("/") or for one folder (e.g. Cache-Control immutable on /assets). An empty
// value removes the header from the answer. Headers for "/" ride on every router that serves the website; a folder
// with its own headers gets a router of its own (above the browser-cache router, below protected folders).
//
// Allow-list: when the list holds addresses, only those reach the website; everyone else gets 403 from the edge
// (for a website that only a proxy or CDN in front may reach, or one for a few offices).

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	MaxHeaders     = 20
	MaxHeaderPaths = 10
	MaxAllowed     = 200
	maxHeaderValue = 4096
	prioHeaders    = 6000 // + the folder's length; between the cache router (5000) and protected folders (10000)
)

// HeaderRule is one response header of a website: on every answer (Path "/") or on one folder.
type HeaderRule struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Value string `json:"value"` // "" removes the header
}

// HeaderPresets are the headers the panel suggests.
var HeaderPresets = []string{"Content-Security-Policy", "Strict-Transport-Security", "Cache-Control", "X-Frame-Options",
	"X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy", "Cross-Origin-Opener-Policy", "Access-Control-Allow-Origin"}

var (
	headerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	// the edge or the browser needs these as they are
	headerRefused = map[string]bool{"content-length": true, "transfer-encoding": true, "connection": true, "keep-alive": true,
		"upgrade": true, "host": true, "set-cookie": true, "location": true, "content-encoding": true, "trailer": true, "te": true,
		"www-authenticate": true, "proxy-authenticate": true, "date": true, "alt-svc": true}
)

// canonicalHeader writes a header name as browsers show it (content-security-policy -> Content-Security-Policy).
func canonicalHeader(n string) string {
	parts := strings.Split(strings.ToLower(n), "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

func checkHeader(h HeaderRule) (HeaderRule, error) {
	h.Path = cleanFolder(strings.TrimSpace(h.Path))
	if h.Path == "" {
		h.Path = "/"
	}
	if h.Path != "/" {
		if err := checkProtectPath(h.Path); err != nil {
			return h, err
		}
	}
	h.Name = strings.TrimSpace(h.Name)
	if !headerNameRe.MatchString(h.Name) {
		return h, fmt.Errorf("%w: a header name has letters, digits and -", ErrBadInput)
	}
	if headerRefused[strings.ToLower(h.Name)] {
		return h, fmt.Errorf("%w: the %s header cannot be set here", ErrBadInput, h.Name)
	}
	h.Name = canonicalHeader(h.Name)
	h.Value = strings.TrimSpace(h.Value)
	if len(h.Value) > maxHeaderValue {
		return h, fmt.Errorf("%w: a header value has at most %d characters", ErrBadInput, maxHeaderValue)
	}
	for _, r := range h.Value {
		if r < 0x20 || r > 0x7e || strings.ContainsRune("\"\\`$", r) {
			return h, fmt.Errorf("%w: a header value uses printable characters without \" \\ ` or $", ErrBadInput)
		}
	}
	return h, nil
}

// nextHeaders applies a header-set, header-remove, allow-add or allow-remove change.
func (m *Manager) nextHeaders(s Site, next *Site, c SecurityChange) (bool, error) {
	switch c.Op {
	case "header-set":
		h, err := checkHeader(HeaderRule{Path: c.Path, Name: c.Name, Value: c.Value})
		if err != nil {
			return true, err
		}
		hs := append([]HeaderRule(nil), s.Headers...)
		found, paths := false, map[string]bool{h.Path: true}
		for i, x := range hs {
			paths[x.Path] = true
			if x.Path == h.Path && strings.EqualFold(x.Name, h.Name) {
				if x == h {
					return true, ErrNoChange
				}
				hs[i], found = h, true
			}
		}
		if !found {
			if len(hs) >= MaxHeaders {
				return true, fmt.Errorf("%w: at most %d headers", ErrBadInput, MaxHeaders)
			}
			if len(paths) > MaxHeaderPaths {
				return true, fmt.Errorf("%w: headers on at most %d folders", ErrBadInput, MaxHeaderPaths)
			}
			hs = append(hs, h)
		}
		sort.SliceStable(hs, func(i, j int) bool {
			if hs[i].Path != hs[j].Path {
				return hs[i].Path < hs[j].Path
			}
			return hs[i].Name < hs[j].Name
		})
		next.Headers = hs
	case "header-remove":
		var hs []HeaderRule
		for _, x := range s.Headers {
			if !(x.Path == c.Path && strings.EqualFold(x.Name, c.Name)) {
				hs = append(hs, x)
			}
		}
		if len(hs) == len(s.Headers) {
			return true, fmt.Errorf("%w: %s has no header %s", ErrBadInput, c.Path, c.Name)
		}
		next.Headers = hs
	case "allow-add":
		v, err := checkBlock(c.IP)
		if err != nil {
			return true, err
		}
		for _, x := range s.AllowIPs {
			if x == v {
				return true, fmt.Errorf("%w: %s is already allowed", ErrBadInput, v)
			}
		}
		if len(s.AllowIPs) >= MaxAllowed {
			return true, fmt.Errorf("%w: at most %d allowed addresses", ErrBadInput, MaxAllowed)
		}
		next.AllowIPs = append(append([]string(nil), s.AllowIPs...), v)
	case "allow-remove":
		var a []string
		for _, x := range s.AllowIPs {
			if x != c.IP {
				a = append(a, x)
			}
		}
		if len(a) == len(s.AllowIPs) {
			return true, fmt.Errorf("%w: %s is not allowed", ErrBadInput, c.IP)
		}
		next.AllowIPs = a
	default:
		return false, nil
	}
	return true, nil
}

func (s Site) headersMW(i int) string { return fmt.Sprintf("site-%s-hdr%d", s.ID, i) }

// headerPaths lists the folders with headers of their own, "/" first when the website has headers for all answers.
func (s Site) headerPaths() []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range s.Headers {
		if !seen[h.Path] {
			seen[h.Path] = true
			out = append(out, h.Path)
		}
	}
	sort.Strings(out) // "/" sorts first
	return out
}

// siteHeadersMW is the middleware of the headers for all answers ("" when there are none).
func (s Site) siteHeadersMW() string {
	if p := s.headerPaths(); len(p) > 0 && p[0] == "/" {
		return s.headersMW(0)
	}
	return ""
}

// headerEdge renders the header middlewares and the folder routers.
func (s Site) headerEdge(canon, dollar string) ([]edgeRouter, []edgeMiddleware) {
	var rs []edgeRouter
	var ms []edgeMiddleware
	for i, p := range s.headerPaths() {
		mw := edgeMiddleware{name: s.headersMW(i), kind: "headers"}
		for _, h := range s.Headers {
			if h.Path == p {
				mw.hdrs = append(mw.hdrs, [2]string{h.Name, h.Value})
			}
		}
		ms = append(ms, mw)
		if p == "/" {
			continue
		}
		in := hostRule([]string{canon}) + " && (Path(`" + p + "`) || PathPrefix(`" + p + "/`))"
		rs = append(rs, edgeRouter{name: fmt.Sprintf("site-%s-hdrpath%d", s.ID, i), priority: prioHeaders + len(p), rule: in,
			middlewares: s.chainWith(mw.name)})
		if s.Optimize != nil && s.Optimize.CacheDays > 0 { // static files of the folder keep the browser cache
			rs = append(rs, edgeRouter{name: fmt.Sprintf("site-%s-hdrstatic%d", s.ID, i), priority: prioHeaders + 300 + len(p),
				rule:        in + " && PathRegexp(`(?i)[.](" + strings.Join(CacheExts, "|") + ")" + dollar + "`)",
				middlewares: s.chainWith(mw.name, s.cacheMW())})
		}
	}
	return rs, ms
}

// allowRouter sends everyone outside the allow-list to the deny middleware (certificates' /.well-known stays open).
func (s Site) allowRouter(rid string, routed []string, deny string) []edgeRouter {
	if len(s.AllowIPs) == 0 {
		return nil
	}
	ips := make([]string, len(s.AllowIPs))
	for i, ip := range s.AllowIPs {
		ips[i] = "ClientIP(`" + ip + "`)"
	}
	return []edgeRouter{{name: rid + "-allow", priority: prioBlock + 1, middlewares: []string{deny},
		rule: hostRule(routed) + " && !PathPrefix(`/.well-known/`) && !(" + strings.Join(ips, " || ") + ")"}}
}

// xffRule matches a blocked address as the right-most X-Forwarded-For entry, the one a trusted proxy in front
// (SITE-03) appends; the edge drops the header of anyone it does not trust, so a visitor cannot use it. IPv4
// networks become octet patterns; an IPv6 network has no pattern (ClientIP still matches it directly).
func xffRule(v, dollar string) string {
	pat := ""
	if a, err := netip.ParseAddr(v); err == nil {
		if a.Is4() {
			pat = strings.ReplaceAll(a.String(), ".", "[.]")
		} else {
			pat = "(?i)" + a.String()
		}
	} else if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Is4() {
		o := p.Masked().Addr().As4()
		full, rem := p.Bits()/8, p.Bits()%8
		var parts []string
		for i := 0; i < full; i++ {
			parts = append(parts, strconv.Itoa(int(o[i])))
		}
		if full < 4 {
			if rem > 0 {
				var alt []string
				for k := 0; k < 1<<(8-rem); k++ {
					alt = append(alt, strconv.Itoa(int(o[full])+k))
				}
				parts = append(parts, "("+strings.Join(alt, "|")+")")
				full++
			}
			for i := full; i < 4; i++ {
				parts = append(parts, "[0-9]{1,3}")
			}
		}
		pat = strings.Join(parts, "[.]")
	}
	if pat == "" {
		return ""
	}
	return "HeaderRegexp(`X-Forwarded-For`, `(^|,)[ ]*" + pat + "[ ]*" + dollar + "`)"
}
