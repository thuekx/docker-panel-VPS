package sites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	c137       = "1111111111111111111111111111111111111111"
	c138       = "2222222222222222222222222222222222222222"
	c138peeled = "3333333333333333333333333333333333333333"
)

type updHost struct {
	mu    sync.Mutex
	calls []string
	fail  string
}

func (h *updHost) Host(_ context.Context, _ io.Reader, bin string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := hostBins[bin]; !ok {
		return nil, errors.New("not allowed")
	}
	line := bin + " " + strings.Join(args, " ")
	h.calls = append(h.calls, line)
	if h.fail != "" && strings.Contains(line, h.fail) {
		return []byte("Failed to start transient service unit"), errors.New("exit 1")
	}
	if bin == "git" {
		return []byte(c138 + "\trefs/tags/v1.38.0\n" + c138peeled + "\trefs/tags/v1.38.0^{}\n"), nil
	}
	return nil, nil
}

const releasesJSON = `[
 {"tag_name":"v1.38.0","name":"v1.38.0 — update from the panel","body":"### Added\r\n- Update page","published_at":"2026-11-27T10:00:00Z"},
 {"tag_name":"v1.39.0-rc1","name":"rc","body":"","prerelease":true},
 {"tag_name":"v1.40.0","name":"draft","draft":true},
 {"tag_name":"v1.36.0","name":"v1.36.0","body":"old","published_at":"2026-11-26T10:00:00Z"},
 {"tag_name":"v1.37.0","name":"v1.37.0","body":"passkeys","published_at":"2026-11-26T20:00:00Z"},
 {"tag_name":"latest","name":"bad tag"}
]`

// updEnv makes a server on v1.37.0 with its updater in place and a release list on a test server.
func updEnv(t *testing.T) (*Manager, *updHost, string, *int) {
	t.Helper()
	m, _ := newTest(t, nil)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") != "nostek-agent" {
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, releasesJSON)
	}))
	t.Cleanup(srv.Close)
	varLib := t.TempDir()
	for _, p := range []string{"panel-01", "site-01"} {
		os.MkdirAll(filepath.Join(varLib, p), 0o755)
		os.WriteFile(filepath.Join(varLib, p, "state"), []byte("phase=APPLIED\ncommit="+c137+"\nrelease=v1.37.0\n"), 0o644)
	}
	dir := filepath.Join(m.cfg.Data, "src", c137, "packages", "upd-01")
	os.MkdirAll(dir, 0o755)
	script := []byte("#!/usr/bin/env bash\necho updater\n")
	os.WriteFile(filepath.Join(dir, "nostek-update.sh"), script, 0o755)
	sum := sha256.Sum256(script)
	os.WriteFile(filepath.Join(dir, "upd-01.lock"), []byte(hex.EncodeToString(sum[:])+"  nostek-update.sh\n"), 0o644)
	h := &updHost{}
	m.UseUpdateTest(srv.URL, "file:///repo", varLib, h)
	return m, h, filepath.Join(dir, "nostek-update.sh"), &hits
}

func TestUpdateInfo(t *testing.T) {
	m, _, _, hits := updEnv(t)
	v := m.UpdateInfo(context.Background(), false)
	if v.Current != "v1.37.0" || v.Error != "" || v.Ready != "" {
		t.Fatalf("view %+v", v)
	}
	var tags []string
	for _, r := range v.Releases {
		tags = append(tags, r.Tag)
	}
	if strings.Join(tags, ",") != "v1.38.0,v1.37.0,v1.36.0" {
		t.Fatalf("releases (drafts, pre-releases and odd tags left out, newest first): %v", tags)
	}
	if !v.Releases[0].Newer || v.Releases[1].Newer || v.Releases[2].Newer {
		t.Fatalf("newer flags %+v", v.Releases)
	}
	if v.Releases[0].Notes != "### Added\n- Update page" {
		t.Fatalf("notes %q", v.Releases[0].Notes)
	}
	m.UpdateInfo(context.Background(), false)
	if *hits != 1 {
		t.Fatalf("the list is cached: %d calls", *hits)
	}
	m.UpdateInfo(context.Background(), true)
	if *hits != 2 {
		t.Fatalf("fresh skips the cache: %d calls", *hits)
	}
}

func TestUpdateNotReady(t *testing.T) {
	m, _, script, _ := updEnv(t)
	os.WriteFile(script, []byte("#!/usr/bin/env bash\necho changed\n"), 0o755)
	if v := m.UpdateInfo(context.Background(), false); !strings.Contains(v.Ready, "sha256") {
		t.Fatalf("a changed updater: %q", v.Ready)
	}
	if _, err := m.StartUpdate(context.Background(), "v1.38.0", "", "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("start with a changed updater: %v", err)
	}
	os.RemoveAll(filepath.Dir(script))
	if v := m.UpdateInfo(context.Background(), false); !strings.Contains(v.Ready, "runbook") {
		t.Fatalf("no updater: %q", v.Ready)
	}
}

func TestStartUpdate(t *testing.T) {
	m, h, script, _ := updEnv(t)
	ctx := context.Background()
	for _, tag := range []string{"v1.37.0", "v1.36.0", "v1.40.0", "v1.39.0-rc1", "1.38.0", "v1.38.0; reboot"} {
		if _, err := m.StartUpdate(ctx, tag, "", "", ""); !errors.Is(err, ErrBadInput) {
			t.Fatalf("%s: %v", tag, err)
		}
	}
	if _, err := m.StartUpdate(ctx, "v1.38.0", "short", "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("short passphrase: %v", err)
	}
	if _, err := m.StartUpdate(ctx, "v1.38.0", "a long passphrase", "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("backup without the panel's copy: %v", err)
	}
	if len(h.calls) != 0 {
		t.Fatalf("nothing may run before the checks pass: %v", h.calls)
	}
	os.MkdirAll(m.panelDir(), 0o700)
	os.WriteFile(filepath.Join(m.panelDir(), UpdateCopy), []byte("db"), 0o600)
	id, err := m.StartUpdate(ctx, "v1.38.0", "", "v1.37.0", "v1.37.0")
	if err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m, id); j.State != "done" || strings.Join(j.Steps, ",") != "start" {
		t.Fatalf("job %+v", j)
	}
	run := h.calls[len(h.calls)-1]
	host, _ := os.Hostname()
	want := "systemd-run --unit nostek-update-"
	if !strings.HasPrefix(run, want) || !strings.Contains(run, "--collect --quiet -- /usr/bin/bash "+script+" RUN --ack UPD-01 --tag v1.38.0 --commit "+c138peeled+
		" --expect-ip "+ip+" --expect-host "+host+" --domain panel.nostek.dev --preview-zone preview.nostek.dev --db-copy "+filepath.Join(m.panelDir(), UpdateCopy)) {
		t.Fatalf("updater started with %q", run)
	}
	if !strings.HasPrefix(h.calls[0], "git ls-remote --tags file:///repo refs/tags/v1.38.0") {
		t.Fatalf("tag lookup %q", h.calls[0])
	}
	v := m.UpdateInfo(ctx, false)
	if v.Status == nil || v.Status.Phase != "STARTING" || v.Status.Tag != "v1.38.0" || v.Status.From != "v1.37.0" || v.Job == nil {
		t.Fatalf("status %+v", v.Status)
	}
	if fi, _ := os.Stat(filepath.Join(m.updr().dir(), "status")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("status mode %v", fi.Mode())
	}
}

func TestStartUpdateFails(t *testing.T) {
	m, h, _, _ := updEnv(t)
	h.fail = "systemd-run"
	id, err := m.StartUpdate(context.Background(), "v1.38.0", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, m, id)
	if j.State != "failed" || !strings.Contains(j.Error, "did not start: Failed to start") {
		t.Fatalf("job %+v", j)
	}
	if s := m.updr().status(); s != nil {
		t.Fatalf("a start that failed leaves no status: %+v", s)
	}
}

func TestUpdateRunningAndStatus(t *testing.T) {
	m, _, _, _ := updEnv(t)
	u := m.updr()
	os.MkdirAll(u.dir(), 0o700)
	f, _ := os.Create(filepath.Join(u.dir(), "lock"))
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	os.WriteFile(filepath.Join(u.dir(), "status"), []byte("phase=RUNNING\ntag=v1.38.0\nfrom=v1.37.0\nstep=2/4\nstarted=100\n"), 0o600)
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "PASS line")
	}
	os.WriteFile(filepath.Join(u.dir(), "progress"), []byte(strings.Join(lines, "\n")+"\nPROGRESS 2/4 agent v1.38.0\n"), 0o600)
	s := u.status()
	if !s.Running || s.Phase != "RUNNING" || s.Step != "2/4" || s.Started != 100 || len(s.Lines) > 120 || s.Lines[len(s.Lines)-1] != "PROGRESS 2/4 agent v1.38.0" {
		t.Fatalf("status %+v (%d lines)", s, len(s.Lines))
	}
	if _, err := m.StartUpdate(context.Background(), "v1.38.0", "", "", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("second update: %v", err)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if u.status().Running {
		t.Fatal("not running once the lock is free")
	}
}

func TestUpdateGitHubDown(t *testing.T) {
	m, _, _, _ := updEnv(t)
	m.updr().api = "http://127.0.0.1:1/releases"
	v := m.UpdateInfo(context.Background(), true)
	if v.Error == "" || len(v.Releases) != 0 {
		t.Fatalf("view %+v", v)
	}
	if _, err := m.StartUpdate(context.Background(), "v1.38.0", "", "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("start without a list: %v", err)
	}
}

func TestVerNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v1.38.0", "v1.37.0", true}, {"v1.10.0", "v1.9.9", true}, {"v2.0.0", "v1.99.99", true}, {"v1.37.0", "v1.37.0", false},
		{"v1.36.1", "v1.37.0", false}, {"v1.38.0", "", false}, {"x", "v1.0.0", false}} {
		if got := verNewer(c.a, c.b); got != c.want {
			t.Errorf("%s > %s = %v", c.a, c.b, got)
		}
	}
	_ = time.Now
}

func TestStartUpdateWithBackup(t *testing.T) {
	m, h, _, _ := updEnv(t)
	os.MkdirAll(m.panelDir(), 0o700)
	os.WriteFile(filepath.Join(m.panelDir(), PanelCopy), []byte("SQLite format 3\x00 copy"), 0o600)
	id, err := m.StartUpdate(context.Background(), "v1.38.0", serverPass, "v1.37.0", "v1.37.0")
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, m, id)
	if j.State != "done" || len(j.Steps) < 3 || j.Steps[0] != "backup" || j.Steps[len(j.Steps)-1] != "start" || !strings.HasPrefix(j.Steps[len(j.Steps)-2], "done nostek-server-") {
		t.Fatalf("job %+v", j)
	}
	if b, _ := m.ListServerBackups(); len(b.Backups) != 1 {
		t.Fatalf("server backups %+v", b.Backups)
	}
	if !strings.HasPrefix(h.calls[len(h.calls)-1], "systemd-run ") {
		t.Fatalf("calls %v", h.calls)
	}
	if _, err := os.Stat(filepath.Join(m.panelDir(), PanelCopy)); err == nil {
		t.Fatal("the backup copy was left behind")
	}
}
