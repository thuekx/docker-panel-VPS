package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stagingWP plays WP-CLI for a live website and its copy: home follows "option update home", blog_public is kept.
func stagingWP(home string) (func(string) (int, string), func() string) {
	var mu sync.Mutex
	public := "1"
	inner := oldSiteWP(home)
	return func(cmd string) (int, string) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case cmd == "wp option get blog_public":
				return 0, public
			case strings.HasPrefix(cmd, "wp option update blog_public "):
				public = strings.TrimPrefix(cmd, "wp option update blog_public ")
				return 0, "Success"
			}
			return inner(cmd)
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return public
		}
}

func liveSite(t *testing.T) (*Manager, *fakeRunner, string, func() string, *[]string) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}, "staging-voltix-id.preview.nostek.dev": {ip}})
	m.cfg.FileUID = os.Getuid()
	job, ls, err := m.Create(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create: %+v", v)
	}
	id := ls.ID
	ticking(m, time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC))
	wp := m.wpDir(id)
	for p, c := range map[string]string{
		"wp-includes/version.php":    "<?php\n$wp_version = '6.9.1';\n",
		"wp-content/uploads/a.jpg":   "JPEG v1",
		"wp-content/plugins/x/x.php": "<?php // x",
		"wp-config.php":              "<?php define('DB_PASSWORD', 'live-secret');",
		".np-upload-abc":             "partial",
		"wp-content/cache/page.html": "cached",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(wp, p)), 0o755)
		if err := os.WriteFile(filepath.Join(wp, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.dumped = "-- MariaDB dump\nCREATE TABLE wp_posts (id int);\n"
	s, _ := m.Get(id)
	answer, public := stagingWP(s.URL)
	seen := fakeWPCLI(f, answer)
	return m, f, id, public, seen
}

func TestStagingCopyAndPush(t *testing.T) {
	m, f, id, public, seen := liveSite(t)
	// the WordPress image writes the new website's own wp-config.php when it first starts
	sw := m.wpDir("staging-voltix-id")
	cli := f.hook
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "core install") {
			_ = os.WriteFile(filepath.Join(sw, "wp-config.php"), []byte("<?php define('DB_PASSWORD', 'staging-own');"), 0o640)
		}
		return cli(args)
	}
	job, st, err := m.StartStaging(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "staging-voltix-id" || st.Domain != "staging.voltix.id" || st.StagingOf != id {
		t.Fatalf("staging site %+v", st)
	}
	v := waitJob(t, m, job)
	if v.State != "done" || v.Kind != "staging" || v.HasSecret || !has(v.Steps, "copy files") || !has(v.Steps, "copy database") || !has(v.Steps, "addresses") {
		t.Fatalf("job %+v", v)
	}
	if has(v.Steps, "safety files") {
		t.Error("a fresh staging website was backed up first")
	}
	got, err := m.Get(st.ID)
	if err != nil || got.StagingOf != id || got.Phase != "ready" || m.StagingOf(id) != st.ID {
		t.Fatalf("staging record %+v %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(sw, "wp-content/uploads/a.jpg")); string(b) != "JPEG v1" {
		t.Fatalf("files not copied: %q", b)
	}
	for _, p := range []string{".np-upload-abc", "wp-content/cache/page.html", "wp-config.old.php"} {
		if _, err := os.Lstat(filepath.Join(sw, p)); err == nil {
			t.Errorf("%s copied", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(sw, "wp-config.php")); strings.Contains(string(b), "live-secret") || !strings.Contains(string(b), "staging-own") {
		t.Error("the live wp-config.php (its database login) reached the staging website")
	}
	if public() != "0" {
		t.Error("search engines not kept away from the staging website")
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "staging-voltix-id") {
		t.Errorf("addresses not rewritten to the staging website: %v", *seen)
	}
	// one staging copy per website, and a staging website has none of its own
	if _, _, err := m.StartStaging(context.Background(), id); !errors.Is(err, ErrBadInput) {
		t.Errorf("second staging copy: %v", err)
	}
	if _, _, err := m.StartStaging(context.Background(), st.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("staging of a staging website: %v", err)
	}

	// work on the copy, then push it to live; live keeps search engines on
	_ = os.WriteFile(filepath.Join(sw, "wp-content/uploads/a.jpg"), []byte("JPEG v2"), 0o644)
	_ = os.WriteFile(filepath.Join(sw, "wp-content/uploads/new.jpg"), []byte("NEW"), 0o644)
	if _, err := m.StartPush(id, "voltix-id"); !errors.Is(err, ErrBadInput) {
		t.Errorf("push from a website that is not its staging copy: %v", err)
	}
	if _, err := m.StartPush(st.ID, st.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("push into the staging website itself: %v", err)
	}
	m.mu.Lock()
	if _, err := m.StartPush(id, st.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("busy: %v", err)
	}
	m.mu.Unlock()
	setPublic(t, m, id, "1")
	job, err = m.StartPush(id, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || v.Kind != "push" || !has(v.Steps, "safety files") {
		t.Fatalf("push %+v", v)
	}
	lw := m.wpDir(id)
	if b, _ := os.ReadFile(filepath.Join(lw, "wp-content/uploads/a.jpg")); string(b) != "JPEG v2" {
		t.Fatalf("push did not reach live: %q", b)
	}
	if _, err := os.Stat(filepath.Join(lw, "wp-content/uploads/new.jpg")); err != nil {
		t.Error("new file not pushed")
	}
	if public() != "1" {
		t.Error("live lost its search-engine setting")
	}
	b, _ := m.ListBackups(id)
	ok := false
	for _, x := range b.Backups {
		ok = ok || x.Kind == "before-migrate"
	}
	if !ok {
		t.Errorf("no before-migrate backup of live: %+v", b.Backups)
	}
}

// setPublic sets blog_public through the same played WP-CLI.
func setPublic(t *testing.T, m *Manager, id, v string) {
	t.Helper()
	if _, err := m.wp(context.Background(), id, nil, []string{"wp", "option", "update", "blog_public", v}); err != nil {
		t.Fatal(err)
	}
}

func TestStagingRefuses(t *testing.T) {
	m, _, id, _, _ := liveSite(t)
	if _, _, err := m.StartStaging(context.Background(), "nosuch"); err == nil {
		t.Error("unknown website")
	}
	m.mu.Lock()
	if _, _, err := m.StartStaging(context.Background(), id); !errors.Is(err, ErrBusy) {
		t.Errorf("busy: %v", err)
	}
	m.mu.Unlock()
}

func TestJobHold(t *testing.T) {
	j := NewJobs().New("x", "s")
	j.Hold()
	j.Done("pw")
	if v := j.View(); v.State != "running" || v.HasSecret || !j.Held() {
		t.Fatalf("held job ended: %+v", v)
	}
	j.Step("more")
	j.Release()
	if v := j.View(); v.State != "done" || v.HasSecret || j.TakeSecret() != "" {
		t.Fatalf("released %+v", v)
	}
}
