package sites

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeClouDNS answers login and get-zone-info like ClouDNS: one sub-user (42 / its password) and the zones it may use.
func fakeClouDNS(t *testing.T, zones ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.RawQuery != "" {
			http.Error(w, "post a form", 400)
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		if r.PostForm.Get("sub-auth-id") != "42" || r.PostForm.Get("auth-password") != "s3cret-Pass" {
			_, _ = w.Write([]byte(`{"status":"Failed","statusDescription":"Invalid authentication, incorrect auth-id or auth-password."}`))
			return
		}
		switch r.URL.Path {
		case "/dns/login.json":
			_, _ = w.Write([]byte(`{"status":"Success","statusDescription":"Success login."}`))
		case "/dns/get-zone-info.json":
			for _, z := range zones {
				if r.PostForm.Get("domain-name") == z {
					_, _ = w.Write([]byte(`{"name":"` + z + `","type":"master","zone":"domain","status":"1"}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"status":"Failed","statusDescription":"Missing domain-name"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestDNSProviderAndWildcard(t *testing.T) {
	m, f, id := readySite(t)
	m.cfg.PanelUID = os.Getuid()
	srv, seen := fakeClouDNS(t, "voltix.id")
	m.cloudnsURL = srv.URL
	ctx := context.Background()

	if _, err := m.StartWildcard(ctx, id, "voltix.id"); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "sub-user first") {
		t.Fatalf("wildcard without a provider: %v", err)
	}
	for name, c := range map[string][2]string{"not a number": {"abc", "s3cret-Pass"}, "empty": {"42", ""}, "line break": {"42", "a\nb"}} {
		if err := m.SetDNS(ctx, c[0], c[1]); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := m.SetDNS(ctx, "42", "wrong")
	if !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "Invalid authentication") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("bad password: %v", err)
	}
	if c, _ := m.DNS(); c.Configured {
		t.Fatal("a refused sub-user was kept")
	}
	n := len(f.calls)
	if err := m.SetDNS(ctx, " 42 ", "s3cret-Pass"); err != nil {
		t.Fatal(err)
	}
	if j := strings.Join(f.calls[len(f.calls)-1], " "); len(f.calls) != n+1 || j != "restart "+edgeContainer {
		t.Fatalf("edge restart: %v", f.calls[n:])
	}
	for _, p := range []string{m.dnsIDFile(), m.dnsPassFile()} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
	}
	if b, _ := os.ReadFile(m.dnsPassFile()); string(b) != "s3cret-Pass" {
		t.Fatalf("password file %q", b)
	}
	c, err := m.DNS()
	if err != nil || !c.Configured || c.Provider != "cloudns" || c.SubAuthID != "42" || c.Since == 0 {
		t.Fatalf("dns %+v %v", c, err)
	}

	// zones: only the main name's zone, and only one ClouDNS knows
	if z := (Site{Hosts: []Host{{Name: "a.b.example.org", Role: "primary", Routed: true}}}).WildcardZones(); strings.Join(z, " ") != "a.b.example.org b.example.org example.org" {
		t.Fatalf("zones %v", z)
	}
	for _, z := range []string{"example.org", "id", "*.voltix.id"} {
		if _, err := m.StartWildcard(ctx, id, z); !errors.Is(err, ErrBadInput) {
			t.Errorf("zone %q: %v", z, err)
		}
	}
	j, err := m.StartWildcard(ctx, id, "Voltix.ID")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, j); v.State != "done" {
		t.Fatalf("wildcard job %+v", v)
	}
	s, _ := m.Get(id)
	if s.Wildcard != "voltix.id" {
		t.Fatalf("site.json wildcard %q", s.Wildcard)
	}
	compose, _ := os.ReadFile(m.dir(id) + "/compose.yml")
	cs := string(compose)
	if !strings.Contains(cs, "tls.certresolver: dns") || !strings.Contains(cs, "tls.domains[0].main: voltix.id") || !strings.Contains(cs, `tls.domains[0].sans: "*.voltix.id"`) {
		t.Fatalf("compose has no wildcard:\n%s", cs)
	}
	for _, h := range s.Hosts {
		if h.Role == "preview" && s.wildcardCovers(h.Name) {
			t.Fatalf("the preview name %s is not in the zone", h.Name)
		}
	}
	for _, h := range s.Hosts {
		if h.Routed && !s.wildcardCovers(h.Name) && !strings.Contains(cs, "tls.certresolver: le") {
			t.Fatalf("%s lost Let's Encrypt over HTTP", h.Name)
		}
	}
	if strings.Contains(cs, "s3cret") {
		t.Fatal("the password is in compose.yml")
	}
	if !strings.Contains(strings.Join(*seen, " "), "/dns/get-zone-info.json") {
		t.Fatal("the zone was not checked at ClouDNS")
	}
	if _, err := m.StartWildcard(ctx, id, "voltix.id"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("same zone twice: %v", err)
	}

	// the provider cannot go while a website uses it
	if err := m.RemoveDNS(ctx); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), id) {
		t.Fatalf("remove while in use: %v", err)
	}
	if c, _ := m.DNS(); strings.Join(c.Sites, ",") != id {
		t.Fatalf("sites %v", c.Sites)
	}
	j, err = m.StartWildcard(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, j); v.State != "done" {
		t.Fatalf("wildcard off %+v", v)
	}
	if compose, _ = os.ReadFile(m.dir(id) + "/compose.yml"); strings.Contains(string(compose), "certresolver: dns") {
		t.Fatal("wildcard still in compose.yml")
	}
	if err := m.RemoveDNS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.dnsPassFile()); err == nil {
		t.Fatal("password file left")
	}
	if err := m.RemoveDNS(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestWildcardTLS(t *testing.T) {
	s := Site{Hosts: []Host{{Name: "shop.nostek.dev", Role: "primary", Routed: true}, {Name: "www.shop.nostek.dev", Role: "alias", Routed: true}, {Name: "x.preview.nostek.dev", Role: "preview", Routed: true}}, Wildcard: "nostek.dev"}
	for name, want := range map[string]bool{"nostek.dev": true, "shop.nostek.dev": true, "www.shop.nostek.dev": false, "x.preview.nostek.dev": false, "evilnostek.dev": false} {
		if got := s.wildcardCovers(name); got != want {
			t.Errorf("covers %s = %v", name, got)
		}
	}
	if y := s.tlsYAML("shop.nostek.dev"); !strings.Contains(y, "certResolver: dns") || !strings.Contains(y, `sans: ["*.nostek.dev"]`) {
		t.Fatalf("yaml %s", y)
	}
	if y := s.tlsYAML("www.shop.nostek.dev"); !strings.Contains(y, "certResolver: le") {
		t.Fatalf("yaml two levels down %s", y)
	}
	s.Hosts[0].Name = "shop.example.org" // the main name moved out of the zone: the wildcard is unused
	if l := s.tlsLabels("r", ""); !strings.Contains(l[0], "certresolver: le") {
		t.Fatalf("labels %v", l)
	}
}
