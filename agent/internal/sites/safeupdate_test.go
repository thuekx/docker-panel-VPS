package sites

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeSite is a WordPress website whose WP-CLI answers through play: health is what `wp eval` sees before and after
// the update, update what the update itself does.
type play struct {
	mu       sync.Mutex
	updated  bool
	before   func() (int, string)
	after    func() (int, string)
	update   func() (int, string)
	onUpdate func()
}

func (p *play) answer(cmd string) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "wp eval "):
		if p.updated {
			return p.after()
		}
		return p.before()
	case strings.Contains(cmd, " update"):
		p.updated = true
		if p.onUpdate != nil {
			p.onUpdate()
		}
		return p.update()
	}
	return 0, "Success"
}

func fine() (int, string) { return 0, "np-code:200" }
func fatal() (int, string) {
	return 255, "PHP Fatal error:  Uncaught Error: Call to undefined function x() in /var/www/html/wp-content/plugins/akismet/akismet.php:12"
}
func ok() (int, string) { return 0, "Success: Updated 1 of 1 plugins." }

func safeSite(t *testing.T, p *play) (*Manager, *fakeRunner, string, *[]string) {
	t.Helper()
	m, f, id := readySite(t)
	ticking(m, time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC))
	wp := m.wpDir(id)
	_ = os.MkdirAll(filepath.Join(wp, "wp-content/plugins/akismet"), 0o755)
	_ = os.WriteFile(filepath.Join(wp, "wp-content/plugins/akismet/akismet.php"), []byte("<?php // 5.3"), 0o644)
	f.dumped = "-- MariaDB dump\nCREATE TABLE wp_posts (id int);\n"
	return m, f, id, fakeWPCLI(f, p.answer)
}

func has(steps []string, prefix string) bool {
	for _, s := range steps {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func backupsOf(t *testing.T, m *Manager, id string) []string {
	t.Helper()
	b, err := m.ListBackups(id)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, x := range b.Backups {
		n = append(n, x.Kind+":"+x.Name)
	}
	return n
}

func TestSafeUpdateHealthy(t *testing.T) {
	m, _, id, seen := safeSite(t, &play{before: fine, after: fine, update: ok})
	job, err := m.StartSafeUpdate(id, SafeUpdate{Kind: "plugin", Do: "update", Names: []string{"akismet"}})
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, job)
	if v.State != "done" || v.Kind != "wp-update" || !has(v.Steps, "check before") || !has(v.Steps, "update") || !has(v.Steps, "check after") || has(v.Steps, "restore") || !has(v.Steps, "done before-update-") {
		t.Fatalf("job %+v", v)
	}
	if b := backupsOf(t, m, id); len(b) != 1 || !strings.HasPrefix(b[0], "before-update:before-update-") {
		t.Fatalf("backups %v", b)
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "wp plugin update akismet") {
		t.Fatalf("no update ran: %v", *seen)
	}
}

func TestSafeUpdatePutsBackWhenBroken(t *testing.T) {
	var m *Manager
	var id string
	p := &play{before: fine, after: fatal, update: ok}
	p.onUpdate = func() { // the update writes a broken plugin
		_ = os.WriteFile(filepath.Join(m.wpDir(id), "wp-content/plugins/akismet/akismet.php"), []byte("<?php x();"), 0o644)
	}
	m, _, id, _ = safeSite(t, p)
	job, err := m.StartSafeUpdate(id, SafeUpdate{Kind: "plugin", Do: "update", Names: []string{"akismet"}})
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, job)
	if v.State != "failed" || !has(v.Steps, "restore") || !strings.Contains(v.Error, "broke the website") || !strings.Contains(v.Error, "put back") || !strings.Contains(v.Error, "PHP Fatal error") {
		t.Fatalf("job %+v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(m.wpDir(id), "wp-content/plugins/akismet/akismet.php")); string(b) != "<?php // 5.3" {
		t.Fatalf("files not put back: %q", b)
	}
	// the before-update backup stays, so the update can be looked at again later
	if b := backupsOf(t, m, id); len(b) != 1 || !strings.HasPrefix(b[0], "before-update:") {
		t.Fatalf("backups %v", b)
	}
}

func TestSafeUpdateServerErrorPutsBack(t *testing.T) {
	m, _, id, _ := safeSite(t, &play{before: fine, after: func() (int, string) { return 0, "np-code:500" }, update: ok})
	job, _ := m.StartSafeUpdate(id, SafeUpdate{Kind: "core", Do: "update"})
	if v := waitJob(t, m, job); v.State != "failed" || !has(v.Steps, "restore") || !strings.Contains(v.Error, "answers 500") {
		t.Fatalf("job %+v", v)
	}
}

func TestSafeUpdateAlreadyBroken(t *testing.T) {
	m, _, id, _ := safeSite(t, &play{before: fatal, after: fatal, update: ok})
	job, _ := m.StartSafeUpdate(id, SafeUpdate{Kind: "theme", Do: "update", All: true})
	if v := waitJob(t, m, job); v.State != "done" || has(v.Steps, "restore") {
		t.Fatalf("a website that was broken before is rolled back: %+v", v)
	}
}

func TestSafeUpdateFailedButWorks(t *testing.T) {
	m, _, id, _ := safeSite(t, &play{before: fine, after: fine, update: func() (int, string) { return 1, "Error: Could not create directory." }})
	job, _ := m.StartSafeUpdate(id, SafeUpdate{Kind: "plugin", Do: "update", Names: []string{"akismet"}})
	if v := waitJob(t, m, job); v.State != "failed" || has(v.Steps, "restore") || !strings.Contains(v.Error, "still works") || !strings.Contains(v.Error, "before-update-") {
		t.Fatalf("job %+v", v)
	}
}

func TestSafeUpdateRefuses(t *testing.T) {
	m, _, id, _ := safeSite(t, &play{before: fine, after: fine, update: ok})
	for _, u := range []SafeUpdate{
		{Kind: "x", Do: "update"},
		{Kind: "core", Do: "update", Names: []string{"a"}},
		{Kind: "core", Do: "rm"},
		{Kind: "plugin", Do: "delete", Names: []string{"akismet"}},
		{Kind: "plugin", Do: "update"},
		{Kind: "plugin", Do: "update", Names: []string{"../x"}},
	} {
		if _, err := m.StartSafeUpdate(id, u); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", u, err)
		}
	}
	if _, err := m.StartSafeUpdate("nope", SafeUpdate{Kind: "core", Do: "update"}); err == nil {
		t.Error("unknown website accepted")
	}
	m.mu.Lock()
	if _, err := m.StartSafeUpdate(id, SafeUpdate{Kind: "core", Do: "update"}); !errors.Is(err, ErrBusy) {
		t.Errorf("busy: %v", err)
	}
	m.mu.Unlock()
	if b := backupsOf(t, m, id); len(b) != 0 {
		t.Fatalf("a refused update made a backup: %v", b)
	}
}

func TestBrokeBy(t *testing.T) {
	ok, bad := siteHealth{Loads: true, Code: 200}, siteHealth{Why: "fatal"}
	for _, c := range []struct {
		b, a siteHealth
		want bool
	}{
		{ok, ok, false}, {ok, bad, true}, {ok, siteHealth{Loads: true, Code: 502}, true}, {ok, siteHealth{Loads: true, Code: 404}, false},
		{bad, bad, false}, {siteHealth{Loads: true, Code: 500}, siteHealth{Loads: true, Code: 500}, false}, {siteHealth{Loads: true, Code: 500}, bad, true},
	} {
		if got := brokeBy(c.b, c.a); got != c.want {
			t.Errorf("brokeBy(%v, %v) = %v", c.b, c.a, got)
		}
	}
}
