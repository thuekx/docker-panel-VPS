package sites

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestTerminalTarget(t *testing.T) {
	m, _ := newTest(t, nil)
	for _, c := range []struct {
		s    Site
		svc  string
		dir  string
		fail bool
	}{
		{s: redisSite(spec.AppWordPress, "nginx"), svc: "php", dir: "/var/www/html"},
		{s: redisSite(spec.AppWordPress, "apache"), svc: "web", dir: "/var/www/html"},
		{s: redisSite(spec.AppLaravel, "nginx"), svc: "php", dir: "/var/www/html"},
		{s: Site{ID: "a-id", App: spec.AppStatic, Server: "nginx"}, fail: true},
		{s: Site{ID: "a-id", App: spec.AppWordPress, Engine: "native"}, fail: true},
		{s: Site{ID: "a-id", App: spec.AppUptimeKuma}, fail: true},
	} {
		c.s.Redis = nil
		tt, err := m.termTarget(c.s)
		if c.fail != (err != nil) || (!c.fail && (tt.Service != c.svc || tt.Dir != c.dir || strings.HasPrefix(tt.User, "0"))) {
			t.Errorf("%s/%s/%s: %+v %v", c.s.App, c.s.Server, c.s.Engine, tt, err)
		}
	}
}

func TestTerminalOpen(t *testing.T) {
	m, _, id := readySite(t)
	if _, err := m.OpenTerminal(context.Background(), id); !errors.Is(err, ErrBadInput) {
		t.Fatalf("open while off: %v", err)
	}
	if v, _ := m.Terminal(id); !v.Possible || v.On || v.Target.Service == "" || v.Max != TermPerSite {
		t.Fatalf("view %+v", v)
	}
	if err := m.SetTerminal(id, false); !errors.Is(err, ErrBadInput) {
		t.Fatalf("off twice: %v", err)
	}
	if err := m.SetTerminal(id, true); err != nil {
		t.Fatal(err)
	}
	var got []string
	m.UseTermCmd(func(ctx context.Context, dir string, args []string) *exec.Cmd {
		got = args
		return exec.Command("true")
	})
	a, err := m.OpenTerminal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(got, " ")
	for _, want := range []string{"compose --project-directory", "-p " + Project(id), "exec -u ", "-e TERM=xterm-256color", " sh -c cd /var/www/html"} {
		if !strings.Contains(j, want) {
			t.Errorf("command lacks %q: %s", want, j)
		}
	}
	if strings.Contains(j, " -T ") || strings.Contains(j, "-u 0") || strings.Contains(j, "--privileged") {
		t.Fatalf("command %s", j)
	}
	b, err := m.OpenTerminal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenTerminal(context.Background(), id); !errors.Is(err, ErrBusy) {
		t.Fatalf("third session: %v", err)
	}
	if v, _ := m.Terminal(id); v.Open != 2 {
		t.Fatalf("open %d", v.Open)
	}
	a.Done()
	b.Done()
	if v, _ := m.Terminal(id); v.Open != 0 {
		t.Fatalf("open after done %d", v.Open)
	}
	if err := m.SetTerminal(id, false); err != nil {
		t.Fatal(err)
	}
}
