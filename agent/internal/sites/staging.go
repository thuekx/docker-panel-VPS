package sites

// Staging copy (WP-02 wave 2). A WordPress website gets one staging website: a new WordPress website named
// staging.<domain>, made like any other (same engine, web server and PHP), that takes a copy of the live files and
// database through the MIG-01 import core (addresses rewritten, table prefix carried, the panel's own wp-config.php
// kept). Search engines are told to stay away from it. "Push to live" runs the same import the other way, after a
// before-migrate backup of the live website, and keeps the live website's search-engine setting.

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// StagingPrefix is the first label of a staging website's domain.
const StagingPrefix = "staging."

// stagingSpec is the create request of the staging website of live.
func stagingSpec(live Site) spec.Create {
	title := live.Title
	if title == "" {
		title = live.Domain
	}
	return spec.Create{Domain: StagingPrefix + live.Domain, Primary: "domain", Server: live.Server, PHP: live.PHP,
		AdminEmail: live.AdminEmail, Title: title + " (staging)", Engine: live.Engine}
}

// StagingOf returns the id of the staging website of live ("" when it has none).
func (m *Manager) StagingOf(live string) string {
	list, err := m.List()
	if err != nil {
		return ""
	}
	for _, s := range list {
		if s.StagingOf == live {
			return s.ID
		}
	}
	return ""
}

// StartStaging makes the staging website of a WordPress website and copies the live website into it. It returns
// the job (kind "staging", on the staging website) and the staging website.
func (m *Manager) StartStaging(ctx context.Context, id string) (string, Site, error) {
	live, err := m.readySite(id)
	if err != nil {
		return "", Site{}, err
	}
	if !live.IsWP() {
		return "", Site{}, fmt.Errorf("%w: only a WordPress website has a staging copy", ErrBadInput)
	}
	if live.StagingOf != "" {
		return "", Site{}, fmt.Errorf("%w: this is a staging website already", ErrBadInput)
	}
	if other := m.StagingOf(id); other != "" {
		return "", Site{}, fmt.Errorf("%w: this website has a staging copy already (%s); delete it first to make a new one", ErrBadInput, other)
	}
	if !m.mu.TryLock() {
		return "", Site{}, ErrBusy
	}
	s, err := m.Plan(ctx, stagingSpec(live))
	if err != nil {
		m.mu.Unlock()
		return "", Site{}, err
	}
	s.StagingOf = live.ID
	j := m.jobs.New("staging", s.ID)
	j.Hold()
	go func() {
		defer m.mu.Unlock()
		m.create(j, s)
		if !j.Held() {
			return // the create failed and said why
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		fresh, err := m.Get(s.ID)
		if err != nil {
			j.Fail(err.Error())
			return
		}
		mg := &migration{m: m, j: j, s: fresh}
		defer mg.cleanup()
		if err := mg.fromSite(ctx, live, false); err != nil {
			j.Fail("copying the live website failed: " + err.Error() + " — the staging website can be deleted and made again")
			return
		}
		// search engines stay away from the copy
		_, _ = m.wp(ctx, s.ID, nil, []string{"wp", "option", "update", "blog_public", "0"})
		j.Step("done " + mg.summary())
		j.Release()
	}()
	return j.ID, s, nil
}

// StartPush copies a staging website over its live website ("push to live"); id is the live website.
func (m *Manager) StartPush(id, from string) (string, error) {
	live, err := m.migrateSite(id)
	if err != nil {
		return "", err
	}
	src, err := m.readySite(from)
	if err != nil {
		return "", err
	}
	if src.StagingOf != live.ID {
		return "", fmt.Errorf("%w: %s is not the staging copy of %s", ErrBadInput, from, id)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("push", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		// the live website's search-engine setting stays (the copy has search engines off)
		public := ""
		if r, err := m.wp(ctx, id, nil, []string{"wp", "option", "get", "blog_public"}); err == nil && len(r) == 1 && r[0].RC == 0 {
			public = strings.TrimSpace(r[0].Out)
		}
		mg := &migration{m: m, j: j, s: live}
		defer mg.cleanup()
		if err := mg.fromSite(ctx, src, true); err != nil {
			if mg.safe != "" {
				err = fmt.Errorf("%w — the live website can be put back with the backup %s", err, mg.safe)
			}
			j.Fail(err.Error())
			return
		}
		if public == "0" || public == "1" {
			_, _ = m.wp(ctx, id, nil, []string{"wp", "option", "update", "blog_public", public})
		}
		j.Step("done " + mg.summary())
		j.Done("")
	}()
	return j.ID, nil
}

// fromSite stages another website of this server as the source of an import: its files (as they are now) and a
// consistent dump of its database. The caller holds m.mu.
func (mg *migration) fromSite(ctx context.Context, src Site, safety bool) error {
	m := mg.m
	mg.prefix = "wp_"
	if src.TablePrefix != "" {
		mg.prefix = src.TablePrefix
	}
	if !prefixRe.MatchString(mg.prefix) {
		return fmt.Errorf("%w: the table prefix %q cannot be used", ErrBadInput, mg.prefix)
	}
	if v, err := os.ReadFile(filepath.Join(m.wpDir(src.ID), "wp-includes", "version.php")); err == nil {
		if mm := versionRe.FindSubmatch(v); mm != nil {
			mg.version = string(mm[1])
		}
	}
	need := m.siteBytes(src.ID)
	if free := freeBytes(m.cfg.Data); free >= 0 && free < need*2+(1<<30) {
		return fmt.Errorf("%w: not enough free disk for the copy (%d MB free, about %d MB needed)", ErrBadInput, free>>20, (need*2+(1<<30))>>20)
	}
	if err := mg.open(); err != nil {
		return err
	}
	if safety { // a fresh staging website has nothing to keep
		if err := mg.safety(ctx); err != nil {
			return err
		}
	}
	mg.j.Step("copy files")
	base := m.wpDir(src.ID)
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// the target keeps the panel's own wp-config.php (its own database login): the source's is not copied
		if rel == "wp-config.php" || skipped(rel) || strings.HasPrefix(path0(rel), ".np-") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return mg.put(rel, tar.TypeDir, fi.Mode(), fi.ModTime(), "", 0, nil)
		case fi.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return mg.put(rel, tar.TypeSymlink, fi.Mode(), fi.ModTime(), link, 0, nil)
		case fi.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if mg.files%2000 == 0 {
				mg.j.Progress(fmt.Sprintf("%d files", mg.files))
			}
			return mg.put(rel, tar.TypeReg, fi.Mode(), fi.ModTime(), "", fi.Size(), f)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("copying the files failed: %w", err)
	}
	if _, err := mg.root.Lstat("wp-includes/version.php"); err != nil {
		return fmt.Errorf("%w: the source has no WordPress files", ErrBadInput)
	}
	mg.j.Step("copy database")
	if err := m.dumpFile(ctx, src, mg.dump); err != nil {
		return err
	}
	return mg.install(ctx)
}

// path0 is the last element of a slash path.
func path0(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// dumpFile writes a consistent dump of a website's database to dst (0600).
func (m *Manager) dumpFile(ctx context.Context, s Site, dst string) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	var errOut []byte
	if s.IsNative() {
		errOut, err = m.nativeDumpTo(ctx, s, true, bw)
	} else {
		st, ok := m.run.(Streamer)
		if !ok {
			f.Close()
			return errors.New("database copy is not available")
		}
		dir := m.dir(s.ID)
		args := append([]string{"compose", "--project-directory", dir, "-p", Project(s.ID), "-f", filepath.Join(dir, "compose.yml")},
			m.dumpArgs(s, true)...)
		errOut, err = st.DockerTo(ctx, dir, nil, bw, args...)
	}
	if err = errors.Join(err, bw.Flush(), f.Close()); err != nil {
		if len(errOut) > 0 {
			return fmt.Errorf("database copy failed: %w", sqlErr(s, errOut))
		}
		return fmt.Errorf("database copy failed: %w", err)
	}
	return nil
}
