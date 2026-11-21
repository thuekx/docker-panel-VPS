package sites

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptimize(t *testing.T) {
	m, _, id := readySite(t)
	rid := "site-" + id
	for _, bad := range []SecurityChange{{Op: "optimize-set", Days: 3}, {Op: "optimize-set", Days: -1}} {
		if _, err := m.ChangeSecurity(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "optimize-set"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("off while off: %v", err)
	}
	secDo(t, m, id, SecurityChange{Op: "protect-set", Path: "/admin", User: "ana", Hash: hashA})
	secDo(t, m, id, SecurityChange{Op: "optimize-set", Compress: true, Days: 7})
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "optimize-set", Compress: true, Days: 7}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same again: %v", err)
	}
	v, err := m.Perf(id)
	if err != nil || v.PHP == nil || v.PHP.Current != "8.4" || !v.Optimize.Compress || v.Optimize.CacheDays != 7 || v.Job == nil || v.Job.Kind != "optimize" {
		t.Fatalf("perf %+v %v", v, err)
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	for _, want := range []string{
		`traefik.http.middlewares.` + rid + `-compress.compress.includedcontenttypes: "text/html,text/css,`,
		`traefik.http.middlewares.` + rid + `-cache.headers.customresponseheaders.Cache-Control: "public, max-age=604800"`,
		`traefik.http.routers.` + rid + `-voltix-id.middlewares: ` + rid + `-compress` + "\n",
		"traefik.http.routers." + rid + "-static.rule: \"Host(`voltix.id`) && PathRegexp(`(?i)[.](css|js|mjs|png|jpe?g|",
		`|pdf)$$`,
		`traefik.http.routers.` + rid + `-static.priority: "5000"`,
		`traefik.http.routers.` + rid + `-static.middlewares: ` + rid + `-compress,` + rid + `-cache` + "\n",
		`traefik.http.routers.` + rid + `-protect0.middlewares: ` + rid + `-compress,` + rid + `-auth0-r1` + "\n",
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("compose lacks %s\n%s", want, cf)
		}
	}
	// the non-canonical name still only redirects
	if strings.Contains(cf, `-www-voltix-id.middlewares: `+rid+`-compress`) {
		t.Error("redirecting router compresses")
	}
	s, _ := m.Get(id)
	edge := s.NativeEdge("172.18.0.1")
	for _, want := range []string{
		"      middlewares: [" + rid + "-compress]\n",
		"    " + rid + "-compress:\n      compress:\n        includedContentTypes: [text/html, text/css,",
		"    " + rid + "-cache:\n      headers:\n        customResponseHeaders:\n          Cache-Control: \"public, max-age=604800\"\n",
		"|pdf)$`)\"\n      priority: 5000\n",
		"      middlewares: [" + rid + "-compress, " + rid + "-cache]\n",
	} {
		if !strings.Contains(edge, want) {
			t.Errorf("edge lacks %q\n%s", want, edge)
		}
	}
	// cache only, then off
	secDo(t, m, id, SecurityChange{Op: "optimize-set", Days: 30})
	cf = mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	if strings.Contains(cf, "-compress") || !strings.Contains(cf, "max-age=2592000") {
		t.Errorf("cache only:\n%s", cf)
	}
	secDo(t, m, id, SecurityChange{Op: "optimize-set"})
	if s, _ := m.Get(id); s.Optimize != nil || strings.Contains(mustRead(t, filepath.Join(m.dir(id), "compose.yml")), "-static") {
		t.Errorf("off: %+v", s.Optimize)
	}
	if v, _ := m.Security(id); v.Job != nil && v.Job.Kind == "optimize" {
		t.Error("optimize job shown on the Security page")
	}
}
