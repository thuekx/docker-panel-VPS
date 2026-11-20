package sites

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake bcrypt hashes (shape only; the agent never checks a password)
const (
	hashA = "$2y$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	hashB = "$2y$10$bbcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
)

func secDo(t *testing.T, m *Manager, id string, c SecurityChange) {
	t.Helper()
	job, err := m.ChangeSecurity(id, c)
	if err != nil {
		t.Fatalf("%+v: %v", c, err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v: %+v", c, v)
	}
}

func TestCheckBlock(t *testing.T) {
	for in, want := range map[string]string{"203.0.113.7": "203.0.113.7", " 198.51.100.9 ": "198.51.100.9", "203.0.113.77/24": "203.0.113.0/24",
		"2001:db8::1": "2001:db8::1", "2001:db8:1::/48": "2001:db8:1::/48", "10.0.0.0/8": "10.0.0.0/8"} {
		if got, err := checkBlock(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "example.com", "1.2.3", "0.0.0.0/0", "10.0.0.0/7", "::/0", "2001::/15", "fe80::1%eth0", "1.2.3.4/33", "1.2.3.4`"} {
		if _, err := checkBlock(bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSecurityRefuses(t *testing.T) {
	m, _, id := readySite(t)
	for _, c := range []SecurityChange{
		{Op: "block-add", IP: ip},
		{Op: "block-add", IP: "203.0.113.0/24"}, // holds the server's own address
		{Op: "block-add", IP: "nope"},
		{Op: "block-remove", IP: "198.51.100.1"},
		{Op: "protect-set", Path: "/admin", User: "bad user", Hash: hashA},
		{Op: "protect-set", Path: "/admin", User: "ana", Hash: "secret"},
		{Op: "protect-set", Path: "admin", User: "ana", Hash: hashA},
		{Op: "protect-set", Path: "/a/../b", User: "ana", Hash: hashA},
		{Op: "protect-set", Path: "/.well-known/acme-challenge", User: "ana", Hash: hashA},
		{Op: "protect-set", Path: "/a b", User: "ana", Hash: hashA},
		{Op: "protect-remove", Path: "/none"},
		{Op: "hotlink-on", Allow: []string{"bad host"}},
		{Op: "what"},
	} {
		if _, err := m.ChangeSecurity(id, c); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "hotlink-off"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("hotlink-off when off: %v", err)
	}
	if _, err := m.ChangeSecurity("nope", SecurityChange{Op: "block-add", IP: "198.51.100.1"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown site: %v", err)
	}
}

func TestSecurityLabelsAndFiles(t *testing.T) {
	m, _, id := readySite(t)
	m.cfg.FileUID = os.Getuid()
	secDo(t, m, id, SecurityChange{Op: "block-add", IP: "198.51.100.9"})
	secDo(t, m, id, SecurityChange{Op: "block-add", IP: "2001:db8::/32"})
	secDo(t, m, id, SecurityChange{Op: "protect-set", Path: "/admin/", User: "ana", Hash: hashA})
	secDo(t, m, id, SecurityChange{Op: "protect-set", Path: "/admin", User: "budi", Hash: hashB})
	secDo(t, m, id, SecurityChange{Op: "hotlink-on", Allow: []string{"Google.com", "google.com", " "}})
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "block-add", IP: "198.51.100.9"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("same address twice: %v", err)
	}
	s, _ := m.Get(id)
	if len(s.BlockIPs) != 2 || len(s.Protected) != 1 || s.Protected[0].Path != "/admin" || strings.Join(s.Protected[0].Users, ",") != "ana,budi" ||
		s.Protected[0].Rev != 2 || s.Hotlink == nil || strings.Join(s.Hotlink.Allow, ",") != "google.com" {
		t.Fatalf("site %+v %+v %+v", s.BlockIPs, s.Protected, s.Hotlink)
	}
	// the hashes are only in the edge's password file, never in site.json
	if sj := mustRead(t, filepath.Join(m.dir(id), "site.json")); strings.Contains(sj, "$2y$") {
		t.Errorf("site.json holds a hash: %s", sj)
	}
	af := m.authFile(id, "/admin")
	if got := mustRead(t, af); got != "ana:"+hashA+"\nbudi:"+hashB+"\n" {
		t.Errorf("password file %q", got)
	}
	if st, _ := os.Stat(af); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	rid := "site-" + id
	for _, want := range []string{
		`traefik.http.middlewares.` + rid + `-deny.ipallowlist.sourcerange: "127.0.0.2/32"`,
		"traefik.http.routers." + rid + "-block.rule: \"Host(`voltix.id`) && (ClientIP(`198.51.100.9`) || ClientIP(`2001:db8::/32`))\"",
		`traefik.http.routers.` + rid + `-block.priority: "30000"`,
		`traefik.http.routers.` + rid + `-block.middlewares: ` + rid + `-deny`,
		"traefik.http.routers." + rid + "-hotlink.rule: \"Host(`voltix.id`) && PathRegexp(`(?i)[.](jpg|jpeg|png|gif|webp|avif|svg|bmp|ico|mp4|webm|mp3)$$`) && HeaderRegexp(`Referer`, `.+`) && !HeaderRegexp(`Referer`, `^https?://(voltix[.]id|google[.]com)(:[0-9]+)?(/|$$)`)\"",
		`traefik.http.middlewares.` + rid + `-auth0-r2.basicauth.usersfile: "/dynamic/auth/` + filepath.Base(af) + `"`,
		"traefik.http.routers." + rid + "-protect0.rule: \"Host(`voltix.id`) && (Path(`/admin`) || PathPrefix(`/admin/`))\"",
		`traefik.http.routers.` + rid + `-protect0.priority: "10006"`,
		`traefik.http.routers.` + rid + `-protect0.middlewares: ` + rid + `-auth0-r2`,
		`traefik.http.routers.` + rid + `-protect0.service: ` + rid,
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("compose lacks %s\n%s", want, cf)
		}
	}
	edge := s.NativeEdge("172.18.0.1")
	for _, want := range []string{
		"    " + rid + "-block:\n      rule: \"Host(`voltix.id`) && (ClientIP(`198.51.100.9`) || ClientIP(`2001:db8::/32`))\"\n      priority: 30000\n",
		"(/|$)`)\"",
		"    " + rid + "-deny:\n      ipAllowList:\n        sourceRange: [\"127.0.0.2/32\"]\n",
		"    " + rid + "-auth0-r2:\n      basicAuth:\n        usersFile: \"/dynamic/auth/" + filepath.Base(af) + "\"\n",
		"      middlewares: [" + rid + "-auth0-r2]\n",
	} {
		if !strings.Contains(edge, want) {
			t.Errorf("edge lacks %q\n%s", want, edge)
		}
	}
	// a user goes, then the folder; the file goes with the folder
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "protect-user-remove", Path: "/admin", User: "zed"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("unknown user: %v", err)
	}
	secDo(t, m, id, SecurityChange{Op: "protect-user-remove", Path: "/admin", User: "ana"})
	if _, err := m.ChangeSecurity(id, SecurityChange{Op: "protect-user-remove", Path: "/admin", User: "budi"}); !errors.Is(err, ErrBadInput) {
		t.Errorf("last user: %v", err)
	}
	if got := mustRead(t, af); got != "budi:"+hashB+"\n" {
		t.Errorf("password file %q", got)
	}
	secDo(t, m, id, SecurityChange{Op: "protect-remove", Path: "/admin"})
	secDo(t, m, id, SecurityChange{Op: "block-remove", IP: "198.51.100.9"})
	secDo(t, m, id, SecurityChange{Op: "block-remove", IP: "2001:db8::/32"})
	secDo(t, m, id, SecurityChange{Op: "hotlink-off"})
	if _, err := os.Stat(af); !os.IsNotExist(err) {
		t.Errorf("password file still there: %v", err)
	}
	s, _ = m.Get(id)
	if s.BlockIPs != nil || s.Protected != nil || s.Hotlink != nil {
		t.Errorf("left %+v %+v %+v", s.BlockIPs, s.Protected, s.Hotlink)
	}
	if cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml")); strings.Contains(cf, "-deny") || strings.Contains(cf, "-protect") || strings.Contains(cf, "-hotlink") {
		t.Errorf("rules left:\n%s", cf)
	}
	v, _ := m.Security(id)
	if v.Job == nil || v.Job.Kind != "security" || len(v.Blocked) != 0 || len(v.HotlinkExts) == 0 {
		t.Errorf("view %+v", v)
	}
}

func TestSecurityRollsBack(t *testing.T) {
	m, f, id := readySite(t)
	m.cfg.FileUID = os.Getuid()
	secDo(t, m, id, SecurityChange{Op: "protect-set", Path: "/admin", User: "ana", Hash: hashA})
	af := m.authFile(id, "/admin")
	f.fail = "up -d"
	job, err := m.ChangeSecurity(id, SecurityChange{Op: "protect-set", Path: "/admin", User: "ana", Hash: hashB})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" {
		t.Fatalf("%+v", v)
	}
	if got := mustRead(t, af); got != "ana:"+hashA+"\n" {
		t.Errorf("password file not put back: %q", got)
	}
	f.fail = ""
	job, _ = m.ChangeSecurity(id, SecurityChange{Op: "protect-set", Path: "/new", User: "ana", Hash: hashB})
	waitJob(t, m, job)
	f.fail = "up -d"
	job, _ = m.ChangeSecurity(id, SecurityChange{Op: "protect-set", Path: "/other", User: "ana", Hash: hashB})
	waitJob(t, m, job)
	if _, err := os.Stat(m.authFile(id, "/other")); !os.IsNotExist(err) {
		t.Errorf("new password file left after a failure: %v", err)
	}
	if s, _ := m.Get(id); len(s.Protected) != 2 {
		t.Errorf("site.json changed: %+v", s.Protected)
	}
}
