package sites

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadersAndAllowList(t *testing.T) {
	m, _, id := readySite(t)
	rid := "site-" + id
	for _, bad := range []SecurityChange{
		{Op: "header-set", Path: "/", Name: "Set-Cookie", Value: "a=1"},
		{Op: "header-set", Path: "/", Name: "Bad Name", Value: "x"},
		{Op: "header-set", Path: "/", Name: "X-A", Value: "say \"hi\""},
		{Op: "header-set", Path: "/", Name: "X-A", Value: "line\r\nX-B: 1"},
		{Op: "header-set", Path: "/", Name: "X-A", Value: "$HOME"},
		{Op: "header-set", Path: "/.well-known", Name: "X-A", Value: "1"},
		{Op: "header-set", Path: "assets", Name: "X-A", Value: "1"},
		{Op: "header-remove", Path: "/", Name: "X-None"},
		{Op: "allow-add", IP: "0.0.0.0/0"},
		{Op: "allow-add", IP: "nope"},
		{Op: "allow-remove", IP: "203.0.113.9"},
	} {
		if _, err := m.ChangeSecurity(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	secDo(t, m, id, SecurityChange{Op: "optimize-set", Days: 7})
	secDo(t, m, id, SecurityChange{Op: "protect-set", Path: "/admin", User: "ana", Hash: hashA})
	secDo(t, m, id, SecurityChange{Op: "header-set", Path: "/", Name: "content-security-policy", Value: "default-src 'self'"})
	secDo(t, m, id, SecurityChange{Op: "header-set", Path: "/", Name: "X-Powered-By", Value: ""})
	secDo(t, m, id, SecurityChange{Op: "header-set", Path: "/assets/", Name: "Cache-Control", Value: "public, max-age=31536000, immutable"})
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "header-set", Path: "/assets", Name: "cache-control", Value: "public, max-age=31536000, immutable"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same header again: %v", err)
	}
	secDo(t, m, id, SecurityChange{Op: "allow-add", IP: "198.51.100.0/24"})
	secDo(t, m, id, SecurityChange{Op: "allow-add", IP: "2001:db8::1"})
	v, _ := m.Security(id)
	if len(v.Headers) != 3 || v.Headers[0].Name != "Content-Security-Policy" || v.Headers[2].Path != "/assets" || len(v.Allowed) != 2 || len(v.HeaderPresets) == 0 {
		t.Fatalf("view %+v %v", v.Headers, v.Allowed)
	}
	if p, _ := m.Perf(id); p.Job == nil || p.Job.Kind != "optimize" {
		t.Error("a header change is not shown on the Performance page")
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	for _, want := range []string{
		`traefik.http.middlewares.` + rid + `-hdr0.headers.customresponseheaders.Content-Security-Policy: "default-src 'self'"`,
		`traefik.http.middlewares.` + rid + `-hdr0.headers.customresponseheaders.X-Powered-By: ""`,
		`traefik.http.middlewares.` + rid + `-hdr1.headers.customresponseheaders.Cache-Control: "public, max-age=31536000, immutable"`,
		// the website's headers ride on every router; a folder's own and the browser cache come first
		`traefik.http.routers.` + rid + `-voltix-id.middlewares: ` + rid + `-hdr0` + "\n",
		`traefik.http.routers.` + rid + `-protect0.middlewares: ` + rid + `-auth0-r1,` + rid + `-hdr0` + "\n",
		`traefik.http.routers.` + rid + `-static.middlewares: ` + rid + `-cache,` + rid + `-hdr0` + "\n",
		"traefik.http.routers." + rid + "-hdrpath1.rule: \"Host(`voltix.id`) && (Path(`/assets`) || PathPrefix(`/assets/`))\"",
		`traefik.http.routers.` + rid + `-hdrpath1.priority: "6007"`,
		`traefik.http.routers.` + rid + `-hdrpath1.middlewares: ` + rid + `-hdr1,` + rid + `-hdr0` + "\n",
		`traefik.http.routers.` + rid + `-hdrstatic1.priority: "6307"`,
		`traefik.http.routers.` + rid + `-hdrstatic1.middlewares: ` + rid + `-hdr1,` + rid + `-cache,` + rid + `-hdr0` + "\n",
		"traefik.http.routers." + rid + "-allow.rule: \"Host(`voltix.id`) && !PathPrefix(`/.well-known/`) && !(ClientIP(`198.51.100.0/24`) || ClientIP(`2001:db8::1`))\"",
		`traefik.http.routers.` + rid + `-allow.priority: "30001"`,
		`traefik.http.routers.` + rid + `-allow.middlewares: ` + rid + `-deny` + "\n",
		`traefik.http.middlewares.` + rid + `-deny.ipallowlist.sourcerange: "127.0.0.2/32"`,
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("compose lacks %s", want)
		}
	}
	s, _ := m.Get(id)
	edge := s.NativeEdge("172.18.0.1")
	for _, want := range []string{
		"    " + rid + "-hdr0:\n      headers:\n        customResponseHeaders:\n          Content-Security-Policy: \"default-src 'self'\"\n          X-Powered-By: \"\"\n",
		"      middlewares: [" + rid + "-hdr1, " + rid + "-hdr0]\n",
		"!(ClientIP(`198.51.100.0/24`) || ClientIP(`2001:db8::1`))\"\n      priority: 30001\n",
	} {
		if !strings.Contains(edge, want) {
			t.Errorf("edge lacks %q\n%s", want, edge)
		}
	}
	// removing everything leaves no router behind
	for _, c := range []SecurityChange{{Op: "header-remove", Path: "/", Name: "Content-Security-Policy"}, {Op: "header-remove", Path: "/", Name: "X-Powered-By"},
		{Op: "header-remove", Path: "/assets", Name: "Cache-Control"}, {Op: "allow-remove", IP: "198.51.100.0/24"}, {Op: "allow-remove", IP: "2001:db8::1"},
		{Op: "protect-remove", Path: "/admin"}} {
		secDo(t, m, id, c)
	}
	cf = mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	for _, bad := range []string{"-hdr", "-allow", "-deny"} {
		if strings.Contains(cf, rid+bad) {
			t.Errorf("compose still holds %s", bad)
		}
	}
}
