package sites

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// dnsTable is a resolver whose answers a test can change; a missing name is NXDOMAIN.
type dnsTable struct {
	mu sync.Mutex
	m  map[string][]string
}

func (d *dnsTable) set(h string, a ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m[h] = a
}

func (d *dnsTable) resolve(_ context.Context, h string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if h == "broken.example" {
		return nil, errors.New("server misbehaving")
	}
	if a, ok := d.m[h]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: h, IsNotFound: true}
}

// previewSite is a WordPress website made while its domain still points to the old host: only the preview name
// is served.
func previewSite(t *testing.T) (*Manager, *fakeRunner, *dnsTable, Site) {
	t.Helper()
	d := &dnsTable{m: map[string][]string{"voltix-id.preview.nostek.dev": {ip}, "voltix.id": {"198.51.100.7"}}}
	m, f := newTest(t, nil)
	m.resolve = d.resolve
	m.cfg.FileUID = os.Getuid()
	job, s, err := m.Create(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create %+v", v)
	}
	if s.Canonical() != "voltix-id.preview.nostek.dev" {
		t.Fatalf("canonical %q", s.Canonical())
	}
	s, _ = m.Get(s.ID)
	return m, f, d, s
}

func TestDomainsCheck(t *testing.T) {
	m, _, d, s := previewSite(t)
	d.set("voltix.id", ip, "198.51.100.7", "2001:db8::7")
	m.tlsAddr = "127.0.0.1:1" // nothing listens: a routed name shows a certificate error
	got, err := m.Domains(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]DomainCheck{}
	for _, h := range got.Hosts {
		by[h.Name] = h
	}
	if h := by["voltix.id"]; h.DNS != DNSMixed || h.Routed || len(h.IPv4) != 2 || len(h.IPv6) != 1 || h.Cert != "" {
		t.Errorf("voltix.id %+v", h)
	}
	if h := by["www.voltix.id"]; h.DNS != DNSNone || h.Role != "www" {
		t.Errorf("www %+v", h)
	}
	if h := by["voltix-id.preview.nostek.dev"]; h.DNS != DNSHere || !h.Routed || h.Cert != "error" {
		t.Errorf("preview %+v", h)
	}
	if got.Canonical != "voltix-id.preview.nostek.dev" || got.PublicIPv4 != ip || got.Redirects == nil {
		t.Errorf("domains %+v", got)
	}
	d.set("voltix.id", "198.51.100.7")
	if c := m.lookup(context.Background(), "voltix.id"); c.DNS != DNSElsewhere {
		t.Errorf("elsewhere %+v", c)
	}
	if c := m.lookup(context.Background(), "broken.example"); c.DNS != DNSError || c.Error == "" {
		t.Errorf("error %+v", c)
	}
}

// The migration flow: the site was moved in on its preview name, then DNS is switched and one check routes the
// domain, makes it the main address and rewrites WordPress's addresses.
func TestDomainsRefreshMovesTheMainAddress(t *testing.T) {
	m, f, d, s := previewSite(t)
	seen := fakeWPCLI(f, oldSiteWP("https://voltix-id.preview.nostek.dev"))
	if _, err := m.ChangeDomains(context.Background(), s.ID, DomainChange{Op: "refresh"}); !errors.Is(err, ErrNoChange) {
		t.Fatalf("nothing points here yet: %v", err)
	}
	d.set("voltix.id", ip)
	d.set("www.voltix.id", ip)
	job, err := m.ChangeDomains(context.Background(), s.ID, DomainChange{Op: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, job)
	if v.State != "done" || strings.Join(v.Steps, ",") != "routes,addresses,check" {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if got.URL != "https://voltix.id" || got.Canonical() != "voltix.id" {
		t.Errorf("site %+v", got)
	}
	cf := mustRead(t, filepath.Join(m.dir(s.ID), "compose.yml"))
	for _, want := range []string{"Host(`voltix.id`)", "Host(`www.voltix.id`)", "Host(`voltix-id.preview.nostek.dev`)", `replacement: "https://voltix.id$${1}"`} {
		if !strings.Contains(cf, want) {
			t.Errorf("compose lacks %s", want)
		}
	}
	all := strings.Join(*seen, "\n")
	if !strings.Contains(all, "wp search-replace //voltix-id.preview.nostek.dev //voltix.id") || !strings.Contains(all, "wp option update home https://voltix.id") {
		t.Errorf("addresses not rewritten:\n%s", all)
	}
	if !strings.Contains(calls(f), "up -d --wait") {
		t.Errorf("containers not updated: %s", calls(f))
	}
}

func TestDomainsAddressFailurePutsRoutesBack(t *testing.T) {
	m, f, d, s := previewSite(t)
	fakeWPCLI(f, func(cmd string) (int, string) {
		if strings.HasPrefix(cmd, "wp option get home") {
			return 0, "https://voltix-id.preview.nostek.dev"
		}
		if strings.HasPrefix(cmd, "wp search-replace //voltix-id") {
			return 1, "Error: database gone"
		}
		return 0, "Success"
	})
	d.set("voltix.id", ip)
	job, err := m.ChangeDomains(context.Background(), s.ID, DomainChange{Op: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, job)
	if v.State != "failed" || !strings.Contains(v.Error, "database gone") || !strings.Contains(v.Error, "keeps its previous address") {
		t.Fatalf("job %+v", v)
	}
	got, _ := m.Get(s.ID)
	if got.URL != s.URL || got.Canonical() != "voltix-id.preview.nostek.dev" {
		t.Errorf("site not put back: %+v", got)
	}
	if cf := mustRead(t, filepath.Join(m.dir(s.ID), "compose.yml")); strings.Contains(cf, "Host(`voltix.id`)") {
		t.Error("route of voltix.id left behind")
	}
}

func TestDomainsAddRemove(t *testing.T) {
	m, f, id := readySite(t)
	d := &dnsTable{m: map[string][]string{"voltix.id": {ip}, "shop.voltix.id": {ip}}}
	m.resolve = d.resolve
	ctx := context.Background()
	for _, c := range []DomainChange{
		{Op: "add", Name: "voltix.id"},
		{Op: "add", Name: "panel.nostek.dev"},
		{Op: "add", Name: "x.preview.nostek.dev"},
		{Op: "add", Name: "Bad_Name"},
		{Op: "remove", Name: "voltix.id"},
		{Op: "remove", Name: "voltix-id.preview.nostek.dev"},
		{Op: "remove", Name: "nosuch.id"},
		{Op: "nosuch"},
	} {
		if _, err := m.ChangeDomains(ctx, id, c); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v accepted: %v", c, err)
		}
	}
	job, err := m.ChangeDomains(ctx, id, DomainChange{Op: "add", Name: " Shop.Voltix.ID "})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || strings.Join(v.Steps, ",") != "routes" {
		t.Fatalf("add %+v", v)
	}
	job, _ = m.ChangeDomains(ctx, id, DomainChange{Op: "add", Name: "blog.voltix.id"}) // DNS not set yet
	waitJob(t, m, job)
	s, _ := m.Get(id)
	roles := map[string]Host{}
	for _, h := range s.Hosts {
		roles[h.Name] = h
	}
	if h := roles["shop.voltix.id"]; h.Role != "alias" || !h.Routed {
		t.Errorf("shop %+v", h)
	}
	if h := roles["blog.voltix.id"]; h.Role != "alias" || h.Routed {
		t.Errorf("blog %+v", h)
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	if !strings.Contains(cf, "Host(`shop.voltix.id`)") || strings.Contains(cf, "Host(`blog.voltix.id`)") {
		t.Errorf("routes:\n%s", cf)
	}
	if _, err := m.ChangeDomains(ctx, id, DomainChange{Op: "add", Name: "shop.voltix.id"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("twice: %v", err)
	}
	job, err = m.ChangeDomains(ctx, id, DomainChange{Op: "remove", Name: "shop.voltix.id"})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	if cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml")); strings.Contains(cf, "shop.voltix.id") {
		t.Error("removed name still routed")
	}
	// another website cannot take a name in use
	other, _ := m.Get(id)
	other.ID = "other-id"
	other.Hosts = []Host{{Name: "taken.id", Role: "primary", Routed: true}}
	_ = os.MkdirAll(m.dir("other-id"), 0o755)
	_ = writeSite(m.dir("other-id"), other)
	if _, err := m.ChangeDomains(ctx, id, DomainChange{Op: "add", Name: "taken.id"}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("taken: %v", err)
	}
	// a full website refuses more names
	s, _ = m.Get(id)
	for i := len(s.Hosts); i < 14; i++ {
		s.Hosts = append(s.Hosts, Host{Name: "a" + string(rune('a'+i)) + ".voltix.id", Role: "alias"})
	}
	_ = writeSite(m.dir(id), s)
	if _, err := m.ChangeDomains(ctx, id, DomainChange{Op: "add", Name: "more.voltix.id"}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("full: %v", err)
	}
	_ = f
}

func TestCheckRedirect(t *testing.T) {
	for _, r := range []Redirect{
		{From: "/old", To: "/new", Code: 301},
		{From: "/old/page.html", To: "https://example.com/x?a=1&b=2#top", Code: 302},
		{From: "/", To: "https://example.com", Code: 301},
		{From: "/promo_2026~x", To: "http://example.com:8080/", Code: 302},
	} {
		if err := checkRedirect(r); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	for _, r := range []Redirect{
		{From: "old", To: "/new", Code: 301},
		{From: "/a b", To: "/new", Code: 301},
		{From: "/a//b", To: "/new", Code: 301},
		{From: "/a/../b", To: "/new", Code: 301},
		{From: "/.well-known/acme-challenge/x", To: "/new", Code: 301},
		{From: "/old", To: "/new", Code: 307},
		{From: "/old", To: "/old/", Code: 301},
		{From: "/old", To: "ftp://example.com/", Code: 301},
		{From: "/old", To: "https://exa mple.com/", Code: 301},
		{From: "/old", To: `/new"`, Code: 301},
		{From: "/old", To: "/new$1", Code: 301},
		{From: "/old", To: "/new{x}", Code: 301},
		{From: "/old", To: "/new`id`", Code: 301},
		{From: "/old", To: `/new\x`, Code: 301},
		{From: "/old", To: "https://1.2.3.4/", Code: 301},
		{From: "/old", To: "https://user@example.com/", Code: 301},
		{From: "/old", To: "", Code: 301},
	} {
		if err := checkRedirect(r); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v accepted", r)
		}
	}
	if got := redirRegex(Redirect{From: "/old/page.html/"}, "$$"); got != "^https?://[^/]+/old/page[.]html/?([?].*)?$$" {
		t.Errorf("regex %s", got)
	}
	// SITE-03: a whole folder, the rest of the path kept
	for _, r := range []Redirect{{From: "/kb", To: "https://kb.nostek.dev", Code: 301, Prefix: true}, {From: "/en/kb/", To: "https://kb.nostek.dev/en/", Code: 301, Prefix: true}, {From: "/old", To: "/new", Code: 302, Prefix: true}} {
		if err := checkRedirect(r); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	for _, r := range []Redirect{{From: "/kb", To: "https://kb.nostek.dev/?a=1", Code: 301, Prefix: true}, {From: "/", To: "https://x.example", Code: 301, Prefix: true},
		{From: "/kb", To: "/kb/new", Code: 301, Prefix: true}, {From: "/kb/", To: "/kb", Code: 301, Prefix: true}} {
		if err := checkRedirect(r); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v accepted", r)
		}
	}
	kb := Redirect{From: "/kb/", To: "https://kb.nostek.dev/", Code: 301, Prefix: true}
	re := regexp.MustCompile(strings.ReplaceAll(redirRegex(kb, "$$"), "$$", "$"))
	repl := strings.ReplaceAll(Site{}.redirectTarget(kb, "$$"), "$$", "$")
	for in, want := range map[string]string{
		"https://nostek.dev/kb":            "https://kb.nostek.dev",
		"https://nostek.dev/kb/":           "https://kb.nostek.dev/",
		"https://nostek.dev/kb/x/y/?q=1#z": "https://kb.nostek.dev/x/y/?q=1#z",
		"https://nostek.dev/kbx":           "",
		"https://nostek.dev/a/kb/x":        "",
	} {
		got := ""
		if re.MatchString(in) {
			got = re.ReplaceAllString(in, repl)
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}

func TestRedirectsInLabelsAndEdge(t *testing.T) {
	m, _, id := readySite(t)
	ctx := context.Background()
	for _, c := range []DomainChange{
		{Op: "redirect-add", From: "/old-page", To: "/new-page", Code: 301},
		{Op: "redirect-add", From: "/promo", To: "https://shop.example.com/x?ref=1", Code: 302},
	} {
		job, err := m.ChangeDomains(ctx, id, c)
		if err != nil {
			t.Fatal(err)
		}
		if v := waitJob(t, m, job); v.State != "done" {
			t.Fatal(v)
		}
	}
	if _, err := m.ChangeDomains(ctx, id, DomainChange{Op: "redirect-add", From: "/promo/", To: "/x", Code: 301}); !errors.Is(err, ErrBadInput) {
		t.Errorf("same path twice: %v", err)
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	r0, r1 := "site-"+id+"-r0", "site-"+id+"-r1"
	for _, want := range []string{
		"traefik.http.middlewares." + r0 + `.redirectregex.regex: "^https?://[^/]+/old-page/?([?].*)?$$"`,
		"traefik.http.middlewares." + r0 + `.redirectregex.replacement: "https://voltix.id/new-page$${1}"`,
		"traefik.http.middlewares." + r0 + `.redirectregex.permanent: "true"`,
		"traefik.http.middlewares." + r1 + `.redirectregex.replacement: "https://shop.example.com/x?ref=1"`,
		"traefik.http.middlewares." + r1 + `.redirectregex.permanent: "false"`,
		"traefik.http.routers.site-" + id + "-voltix-id.middlewares: " + r0 + "," + r1,
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("compose lacks %s\n%s", want, cf)
		}
	}
	s, _ := m.Get(id)
	edge := s.NativeEdge("172.18.0.1")
	for _, want := range []string{
		"middlewares: [" + r0 + ", " + r1 + "]",
		"    " + r0 + ":\n      redirectRegex:\n        regex: \"^https?://[^/]+/old-page/?([?].*)?$\"\n        replacement: \"https://voltix.id/new-page${1}\"\n        permanent: true\n",
		"        permanent: false\n  services:",
	} {
		if !strings.Contains(edge, want) {
			t.Errorf("edge lacks %q\n%s", want, edge)
		}
	}
	job, err := m.ChangeDomains(ctx, id, DomainChange{Op: "redirect-remove", From: "/old-page"})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	s, _ = m.Get(id)
	if len(s.Redirects) != 1 || s.Redirects[0].From != "/promo" {
		t.Errorf("redirects %+v", s.Redirects)
	}
	if cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml")); strings.Contains(cf, "old-page") || !strings.Contains(cf, "middlewares: site-"+id+"-r0\n") {
		t.Errorf("after remove:\n%s", cf)
	}
}

func TestDomainsNative(t *testing.T) {
	m, _, h, p := nativeTest(t)
	job, s, err := m.Create(context.Background(), nativeReq(spec.AppStatic))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	d := &dnsTable{m: map[string][]string{"voltix.id": {ip}, "www.voltix.id": {ip}, "shop.voltix.id": {ip}}}
	m.resolve = d.resolve
	job, err = m.ChangeDomains(context.Background(), s.ID, DomainChange{Op: "add", Name: "shop.voltix.id"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v\n%s", v, h.all())
	}
	if n := mustRead(t, p.nginx(s.ID)); !strings.Contains(n, "shop.voltix.id") {
		t.Errorf("nginx %s", n)
	}
	if e := mustRead(t, p.edge(s.ID)); !strings.Contains(e, "Host(`shop.voltix.id`)") {
		t.Errorf("edge %s", e)
	}
	if !strings.HasSuffix(h.all(), "nginx -t -q\nsystemctl reload nginx.service") || strings.Contains(h.all(), "restart "+NativeUnit(s.ID)) {
		t.Errorf("only nginx reloads: %s", h.all())
	}
	// nginx refuses: the previous files come back
	h.fail = "nginx -t"
	job, _ = m.ChangeDomains(context.Background(), s.ID, DomainChange{Op: "remove", Name: "shop.voltix.id"})
	if v := waitJob(t, m, job); v.State != "failed" {
		t.Fatalf("%+v", v)
	}
	if n := mustRead(t, p.nginx(s.ID)); !strings.Contains(n, "shop.voltix.id") {
		t.Error("nginx not put back")
	}
	if got, _ := m.Get(s.ID); len(got.Hosts) != len(s.Hosts)+1 {
		t.Errorf("site.json not put back: %+v", got.Hosts)
	}
}
