package sites

// MIG-01: the HTTPS client that pulls a WordPress site from its old host through the Nostek Migrate plugin. It only
// reaches public addresses: the address is checked on every connection (after DNS, so a redirect or a name that
// changes its answer cannot reach loopback, private networks or this server), and only https is spoken.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// MigrateCode is what the plugin page shows: nm1.<base64url(json{u: home URL, k: key})>.
type MigrateCode struct {
	Home *url.URL
	Key  string
}

// ParseMigrateCode reads a migration code; the old site must be https.
func ParseMigrateCode(code string) (MigrateCode, error) {
	bad := fmt.Errorf("%w: this is not a Nostek Migrate code (copy it again from Tools → Nostek Migrate on the old site)", ErrBadInput)
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "nm1.") || len(code) > 2048 {
		return MigrateCode{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(code[4:], "="))
	if err != nil {
		return MigrateCode{}, bad
	}
	var v struct {
		U string `json:"u"`
		K string `json:"k"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.K) < 40 || len(v.K) > 64 || strings.Trim(v.K, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return MigrateCode{}, bad
	}
	u, err := url.Parse(v.U)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return MigrateCode{}, bad
	}
	if u.Scheme != "https" {
		return MigrateCode{}, fmt.Errorf("%w: the old site must use HTTPS (the code would travel in the clear)", ErrBadInput)
	}
	return MigrateCode{Home: u, Key: v.K}, nil
}

// blockedNets are never reached by a migration pull, whatever a name resolves to.
var blockedNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"), netip.MustParsePrefix("64:ff9b::/96"),
}

// publicAddr is true for an address a migration may connect to: not blocked and not this server, unless the test
// allow-list names it (MigrateTestAllow, never set on a real server).
func (m *Manager) publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range m.cfg.MigrateTestAllow {
		if p.Contains(a) {
			return true
		}
	}
	if !a.IsValid() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedNets {
		if p.Contains(a) {
			return false
		}
	}
	if own, err := netip.ParseAddr(m.cfg.PublicIPv4); err == nil && own == a {
		return false
	}
	return true
}

var errBlockedAddr = errors.New("the old site's address is not a public internet address")

// migrateClient is an https-only client whose every connection is checked by publicAddr.
func (m *Manager) migrateClient() (*http.Client, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !m.publicAddr(ap.Addr()) {
			return errBlockedAddr
		}
		return nil
	}}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if m.cfg.MigrateTestCA != "" {
		pem, err := os.ReadFile(m.cfg.MigrateTestCA)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pem)
		tc.RootCAs = pool
	}
	tr := &http.Transport{
		DialContext: d.DialContext, TLSClientConfig: tc, Proxy: nil, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second, MaxIdleConnsPerHost: 2,
	}
	return &http.Client{Transport: httpsOnly{tr}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("the old site redirected away from https")
		}
		return nil
	}}, nil
}

// httpsOnly refuses any request that is not https, whatever built it.
type httpsOnly struct{ rt http.RoundTripper }

func (h httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, errors.New("only https is used to reach the old site")
	}
	return h.rt.RoundTrip(r)
}

// plugin talks to the Nostek Migrate plugin of one old site.
type plugin struct {
	c    *http.Client
	base string // https://old/wp-json/nostek-migrate/v1 (or ?rest_route= when pretty links are off)
	key  string
	q    bool // base uses ?rest_route=
}

// PluginInfo is what the plugin's info route answers.
type PluginInfo struct {
	Plugin    string        `json:"plugin"`
	WordPress string        `json:"wordpress"`
	PHP       string        `json:"php"`
	Home      string        `json:"home"`
	SiteURL   string        `json:"siteurl"`
	Prefix    string        `json:"prefix"`
	Multisite bool          `json:"multisite"`
	Charset   string        `json:"charset"`
	Tables    []PluginTable `json:"tables"`
}

type PluginTable struct {
	Name  string `json:"name"`
	Rows  int64  `json:"rows"`
	Bytes int64  `json:"bytes"`
}

// PluginEntry is one file or folder the plugin lists.
type PluginEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"` // file | dir | link
	Size   int64  `json:"size"`
	Mode   int64  `json:"mode"`
	MTime  int64  `json:"mtime"`
	Target string `json:"target"`
}

func (p *plugin) url(route string, q url.Values) string {
	if p.q {
		if q == nil {
			q = url.Values{}
		}
		q.Set("rest_route", "/nostek-migrate/v1/"+route)
		return p.base + "?" + q.Encode()
	}
	u := p.base + "/" + route
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// do sends one call, tried up to three times on a network error or a 5xx; 4xx answers are final.
func (p *plugin) do(ctx context.Context, method, route string, q url.Values, body []byte) (*http.Response, error) {
	var last error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(try*try) * 2 * time.Second):
			}
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, p.url(route, q), rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Nostek-Key", p.key)
		req.Header.Set("User-Agent", "NostekPanel-Migrate/1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := p.c.Do(req)
		if err != nil {
			if errors.Is(err, errBlockedAddr) {
				return nil, errBlockedAddr
			}
			last = err
			continue
		}
		if resp.StatusCode >= 500 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			last = fmt.Errorf("the old site answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			var e struct {
				Message string `json:"message"`
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			_ = json.Unmarshal(b, &e)
			if e.Message == "" {
				e.Message = http.StatusText(resp.StatusCode)
			}
			return nil, fmt.Errorf("the old site refused (%d): %s", resp.StatusCode, e.Message)
		}
		return resp, nil
	}
	return nil, last
}

func (p *plugin) json(ctx context.Context, method, route string, q url.Values, body []byte, max int64, v any) error {
	resp, err := p.do(ctx, method, route, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, max)).Decode(v); err != nil {
		return fmt.Errorf("the old site's answer is not what the plugin sends (%v): is Nostek Migrate active?", err)
	}
	return nil
}

// connect finds the plugin's routes (pretty links or ?rest_route=) and reads the site's info.
func (m *Manager) connectPlugin(ctx context.Context, code MigrateCode) (*plugin, PluginInfo, error) {
	c, err := m.migrateClient()
	if err != nil {
		return nil, PluginInfo{}, err
	}
	home := strings.TrimRight(code.Home.String(), "/")
	var info PluginInfo
	var first error
	for _, p := range []*plugin{{c: c, base: home + "/wp-json/nostek-migrate/v1", key: code.Key}, {c: c, base: home + "/", key: code.Key, q: true}} {
		err := p.json(ctx, http.MethodGet, "info", nil, nil, 4<<20, &info)
		if err == nil && info.Plugin != "" {
			return p, info, nil
		}
		if errors.Is(err, errBlockedAddr) {
			return nil, PluginInfo{}, fmt.Errorf("%w: %s does not resolve to a public internet address", ErrBadInput, code.Home.Hostname())
		}
		if first == nil {
			first = err
		}
		if err != nil && strings.Contains(err.Error(), "refused (403)") {
			break // the plugin answered: a second route would only count another wrong key
		}
	}
	if first == nil {
		first = errors.New("no answer from the plugin")
	}
	return nil, PluginInfo{}, fmt.Errorf("cannot reach Nostek Migrate on %s: %w", code.Home.Hostname(), first)
}

// list pages through every entry under the old site's WordPress folder.
func (p *plugin) list(ctx context.Context, max int, each func(PluginEntry) error) error {
	after, n := "", 0
	for {
		var page struct {
			Entries []PluginEntry `json:"entries"`
			More    bool          `json:"more"`
		}
		q := url.Values{}
		if after != "" {
			q.Set("after", after)
		}
		if err := p.json(ctx, http.MethodGet, "files", q, nil, 64<<20, &page); err != nil {
			return err
		}
		for _, e := range page.Entries {
			if n++; n > max {
				return fmt.Errorf("%w: the old site has more than %d files and folders", ErrBadInput, max)
			}
			if err := each(e); err != nil {
				return err
			}
		}
		if !page.More || len(page.Entries) == 0 {
			return nil
		}
		next := page.Entries[len(page.Entries)-1].Path
		if next == after {
			return errors.New("the old site's file list does not move on")
		}
		after = next
	}
}

// batch asks for a set of files as one tar stream; each comes to put with its header.
func (p *plugin) batch(ctx context.Context, paths []string, put func(*tar.Header, io.Reader) error) error {
	body, _ := json.Marshal(map[string][]string{"paths": paths})
	resp, err := p.do(ctx, http.MethodPost, "files", nil, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tr := tar.NewReader(resp.Body)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the old site's file stream broke off: %w", err)
		}
		if err := put(h, tr); err != nil {
			return err
		}
	}
}

// rangeTo copies one byte range of a large file to w.
func (p *plugin) rangeTo(ctx context.Context, path string, off, n int64, w io.Writer) (int64, error) {
	resp, err := p.do(ctx, http.MethodGet, "file", url.Values{"path": {path}, "offset": {strconv.FormatInt(off, 10)}, "length": {strconv.FormatInt(n, 10)}}, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return io.Copy(w, io.LimitReader(resp.Body, n))
}

// tableTo writes one table's SQL to w, piece by piece; it returns the rows copied.
func (p *plugin) tableTo(ctx context.Context, name string, w io.Writer) (int64, error) {
	var rows int64
	after := ""
	for i := 0; ; i++ {
		var piece struct {
			SQL  string  `json:"sql"`
			Rows int64   `json:"rows"`
			Next *string `json:"next"`
		}
		q := url.Values{"name": {name}}
		if after != "" {
			q.Set("after", after)
		}
		if err := p.json(ctx, http.MethodGet, "table", q, nil, 64<<20, &piece); err != nil {
			return rows, fmt.Errorf("table %s: %w", name, err)
		}
		if _, err := io.WriteString(w, piece.SQL); err != nil {
			return rows, err
		}
		rows += piece.Rows
		if piece.Next == nil || *piece.Next == "" {
			return rows, nil
		}
		if *piece.Next == after || i > 10_000_000 {
			return rows, fmt.Errorf("table %s: the old site's rows do not move on", name)
		}
		after = *piece.Next
	}
}

// finish tells the plugin the move is done: it deletes its key.
func (p *plugin) finish(ctx context.Context) error {
	var v struct {
		Done bool `json:"done"`
	}
	return p.json(ctx, http.MethodPost, "finish", nil, []byte("{}"), 4096, &v)
}
