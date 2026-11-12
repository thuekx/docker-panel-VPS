package sites

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// fakeRunner records docker calls and answers ps with a fixed state.
type fakeRunner struct {
	mu     sync.Mutex
	calls  [][]string
	stdin  []string
	fail   string // fail any call containing this word
	ps     string
	ins    string            // docker inspect answer
	stats  string            // docker stats answer
	answer map[string]string // answer for any call containing the key
	dumped string            // what DockerTo writes (export)
	hook   func(args []string) ([]byte, error, bool)
}

func (f *fakeRunner) DockerTo(ctx context.Context, dir string, stdin io.Reader, w io.Writer, args ...string) ([]byte, error) {
	out, err := f.Docker(ctx, dir, stdin, args...)
	if err != nil {
		return out, err
	}
	_, err = io.WriteString(w, f.dumped)
	return nil, err
}

func (f *fakeRunner) Docker(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, string(b))
	}
	j := strings.Join(args, " ")
	if f.hook != nil {
		if out, err, ok := f.hook(args); ok {
			return out, err
		}
	}
	if f.fail != "" && strings.Contains(j, f.fail) {
		return []byte("boom"), errors.New("exit 1")
	}
	for k, v := range f.answer {
		if strings.Contains(j, k) {
			return []byte(v), nil
		}
	}
	if strings.Contains(j, " ps ") {
		return []byte(f.ps), nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		return []byte(f.ins), nil
	}
	if len(args) > 0 && args[0] == "stats" {
		return []byte(f.stats), nil
	}
	return nil, nil
}

const ip = "203.0.113.10"

func resolver(m map[string][]string) Resolver {
	return func(ctx context.Context, h string) ([]string, error) {
		if a, ok := m[h]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
}

func newTest(t *testing.T, dns map[string][]string) (*Manager, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{}
	m := New(Config{Data: t.TempDir(), PublicIPv4: ip, PanelDomain: "panel.nostek.dev", PreviewZone: "preview.nostek.dev"}, f, resolver(dns))
	return m, f
}

func req() spec.Create {
	return spec.Create{Domain: "voltix.id", Primary: "domain", Server: "apache", PHP: "8.4", AdminEmail: "owner@nostek.dev", Title: "Voltix"}
}

func waitJob(t *testing.T, m *Manager, id string) JobView {
	t.Helper()
	for i := 0; i < 200; i++ {
		v := m.Jobs().Get(id).View()
		if v.State != "running" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return JobView{}
}

func TestPlanRoutesOnlyHostsPointingHere(t *testing.T) {
	m, _ := newTest(t, map[string][]string{
		"voltix.id":                    {"198.51.100.7"}, // still at the old host
		"www.voltix.id":                {"198.51.100.7"},
		"voltix-id.preview.nostek.dev": {ip},
	})
	s, err := m.Plan(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "voltix-id" || s.URL != "https://voltix-id.preview.nostek.dev" {
		t.Fatalf("id/url = %s %s", s.ID, s.URL)
	}
	for _, h := range s.Hosts {
		if h.Routed != (h.Role == "preview") {
			t.Errorf("host %s routed=%v", h.Name, h.Routed)
		}
	}
	// nothing points here -> refused
	m2, _ := newTest(t, nil)
	if _, err := m2.Plan(context.Background(), req()); err == nil {
		t.Fatal("plan without any routed host accepted")
	}
	// an A record elsewhere next to ours is not "pointing here"
	m3, _ := newTest(t, map[string][]string{"voltix.id": {ip, "198.51.100.7"}, "voltix-id.preview.nostek.dev": {ip}})
	s3, _ := m3.Plan(context.Background(), req())
	if s3.Hosts[0].Routed {
		t.Error("split A record counted as pointing here")
	}
}

func TestCreateApacheWritesFilesAndInstalls(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}, "www.voltix.id": {ip}, "voltix-id.preview.nostek.dev": {ip}})
	jid, s, err := m.Create(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, jid)
	if v.State != "done" || !v.HasSecret {
		t.Fatalf("job = %+v", v)
	}
	pw := m.Jobs().Get(jid).TakeSecret()
	if len(pw) != 24 || m.Jobs().Get(jid).TakeSecret() != "" {
		t.Fatal("secret not shown exactly once")
	}
	if f.stdin[0] != pw+"\n" {
		t.Fatal("admin password not passed on stdin")
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), pw) {
			t.Fatal("admin password appears in docker arguments")
		}
	}
	dir := filepath.Join(m.cfg.Data, "sites", s.ID)
	cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	for _, want := range []string{"name: np-site-voltix-id", "Host(`voltix.id`)", "Host(`www.voltix.id`)", "redirectregex.replacement: \"https://voltix.id$${1}\"", "internal: true", "wordpress:7.1.4-php8.4-apache@sha256:"} {
		if !strings.Contains(string(cf), want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	if strings.Contains(string(cf), "nginx") {
		t.Error("apache site renders nginx")
	}
	st, _ := os.Stat(filepath.Join(dir, "secrets"))
	if st.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir mode %v", st.Mode().Perm())
	}
	got, _ := m.Get(s.ID)
	if got.Phase != "ready" || got.URL != "https://voltix.id" {
		t.Errorf("site = %+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "site.json"))
	if strings.Contains(string(b), pw) {
		t.Error("site.json holds the password")
	}
	// the same domain again is refused (reserved)
	if _, _, err := m.Create(context.Background(), req()); err == nil {
		t.Error("second site with the same domain accepted")
	}
}

func TestCreateNginxAndFailure(t *testing.T) {
	m, f := newTest(t, map[string][]string{"nostek-id.preview.nostek.dev": {ip}})
	c := req()
	c.Domain, c.Server, c.PHP, c.Primary = "nostek.id", "nginx", "8.1", "www"
	f.fail = " up "
	jid, s, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, jid)
	if v.State != "failed" || !strings.Contains(v.Error, "start containers") || v.HasSecret {
		t.Fatalf("job = %+v", v)
	}
	cf, _ := os.ReadFile(filepath.Join(m.cfg.Data, "sites", s.ID, "compose.yml"))
	for _, want := range []string{"fastcgi", "php:", "6.9.0-php8.1-fpm@sha256:", "nginx:1.30-alpine@sha256:", "./nginx.conf:/etc/nginx/conf.d/default.conf:ro"} {
		if want == "fastcgi" {
			nb, _ := os.ReadFile(filepath.Join(m.cfg.Data, "sites", s.ID, "nginx.conf"))
			if !strings.Contains(string(nb), "fastcgi_pass php:9000") {
				t.Error("nginx.conf lacks fastcgi_pass")
			}
			continue
		}
		if !strings.Contains(string(cf), want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	got, _ := m.Get(s.ID)
	if got.Phase != "failed" {
		t.Errorf("phase = %s", got.Phase)
	}
}

func TestBusyStatusLogsTrashRestorePurge(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix-id.preview.nostek.dev": {ip}})
	jid, s, _ := m.Create(context.Background(), req())
	waitJob(t, m, jid)

	f.ps = `{"Service":"db","State":"running","Health":"healthy"}` + "\n" + `{"Service":"web","State":"running","Health":"healthy"}`
	if st := m.Status(context.Background(), s.ID); st != "running" {
		t.Errorf("status = %s", st)
	}
	f.ps = `{"Service":"db","State":"running","Health":"healthy"}` + "\n" + `{"Service":"web","State":"exited"}`
	if st := m.Status(context.Background(), s.ID); st != "partial" {
		t.Errorf("status = %s", st)
	}
	f.ps = "not json"
	if st := m.Status(context.Background(), s.ID); st != "unknown" {
		t.Errorf("garbage status = %s, want unknown", st)
	}

	if err := m.Action(context.Background(), s.ID, "rm"); err == nil {
		t.Error("unknown action accepted")
	}
	if err := m.Action(context.Background(), "../etc", "stop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("path id: %v", err)
	}
	if _, err := m.Logs(context.Background(), s.ID, "php", 10); err == nil {
		t.Error("php logs on an apache site accepted")
	}
	if _, err := m.Logs(context.Background(), s.ID, "web", 5000); err == nil {
		t.Error("5000 lines accepted")
	}

	m.mu.Lock()
	if err := m.Action(context.Background(), s.ID, "stop"); !errors.Is(err, ErrBusy) {
		t.Errorf("busy not reported: %v", err)
	}
	m.mu.Unlock()

	entry, err := m.Delete(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Error("deleted site still listed")
	}
	tr, _ := m.Trash()
	if len(tr) != 1 || tr[0].Entry != entry || tr[0].Purge-tr[0].Deleted != TrashDays*86400 {
		t.Fatalf("trash = %+v", tr)
	}
	// trashed hosts stay reserved
	if _, err := m.Plan(context.Background(), req()); err == nil {
		t.Error("domain of a trashed site accepted")
	}
	if _, err := m.Restore(context.Background(), "../../etc.123456789"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad entry: %v", err)
	}
	if _, err := m.Restore(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(s.ID); err != nil {
		t.Fatal("restored site missing")
	}

	entry, _ = m.Delete(context.Background(), s.ID)
	if gone, _ := m.Purge(); len(gone) != 0 {
		t.Fatal("fresh trash purged")
	}
	m.now = func() time.Time { return time.Now().Add((TrashDays*24 + 1) * time.Hour) }
	gone, err := m.Purge()
	if err != nil || len(gone) != 1 || gone[0] != entry {
		t.Fatalf("purge = %v %v", gone, err)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Data, "trash", entry)); !os.IsNotExist(err) {
		t.Error("purged entry still on disk")
	}
}

func TestNewIDSuffix(t *testing.T) {
	m, _ := newTest(t, nil)
	os.MkdirAll(filepath.Join(m.cfg.Data, "sites", "voltix-id"), 0o755)
	if id := m.newID("voltix.id"); id != "voltix-id-2" {
		t.Errorf("id = %s", id)
	}
	if id := m.newID(strings.Repeat("a", 40) + ".id"); spec.ID(id) != nil || len(id) > 40 {
		t.Errorf("long id = %s", id)
	}
}
