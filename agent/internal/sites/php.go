package sites

// Change PHP version (PHP-01, like cPanel MultiPHP). WordPress, plain PHP and Laravel websites move to another PHP
// version of the catalogue: a "before-php" backup first, then the website's files are written for the new
// version and its containers (or its native PHP-FPM service) start on it. If the website does not come up
// healthy, the previous files come back and it runs on its previous PHP again. The previous version is kept in
// site.json so the panel can offer the way back in one click.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// PHPChoice is one PHP version a website can move to, as the panel shows it.
type PHPChoice struct {
	Version string `json:"version"`
	EOL     bool   `json:"eol,omitempty"`
	Why     string `json:"why,omitempty"` // not offered: the reason (Laravel's minimum, not installed natively)
}

// PHPState is the PHP version of a website, the versions it can move to and the one it came from.
type PHPState struct {
	Current  string      `json:"current"`
	Previous string      `json:"previous,omitempty"`
	Choices  []PHPChoice `json:"choices"`
	Running  bool        `json:"running"` // a PHP change job of this website is running
}

// phpApp is true for the apps whose PHP version can change.
func phpApp(s Site) bool {
	switch s.AppName() {
	case spec.AppWordPress, spec.AppPHP, spec.AppLaravel:
		return true
	}
	return false
}

// phpWhy tells why a website cannot move to PHP v, or "".
func (m *Manager) phpWhy(s Site, v string) string {
	if s.IsLaravel() {
		if l, ok := catalog.Laravel(s.Version); ok && spec.PHPLess(v, l.MinPHP) {
			return fmt.Sprintf("Laravel %s needs PHP %s or newer", l.Version, l.MinPHP)
		}
	}
	if s.IsNative() && (m.native == nil || !slices.Contains(m.native.PHP, v)) {
		return fmt.Sprintf("PHP %s is not installed for the native engine", v)
	}
	if c, ok := m.ionCache(); s.IonCube && ok && !slices.Contains(c.Versions, v) {
		return fmt.Sprintf("ionCube has no loader for PHP %s yet", v) // PHP-02
	}
	return ""
}

// PerfView is the Performance page of a website: its PHP version (nil when it runs no PHP) and Optimize Website.
type PerfView struct {
	PHP       *PHPState `json:"php"`
	Optimize  Optimize  `json:"optimize"`
	CacheDays []int     `json:"cache_days"`
	Job       *JobView  `json:"job,omitempty"` // the newest PHP change or edge change of the website
	// SITE-03: the website's response headers
	Headers       []HeaderRule `json:"headers"`
	HeaderPresets []string     `json:"header_presets"`
}

// Perf returns the Performance page of a website.
func (m *Manager) Perf(id string) (PerfView, error) {
	s, err := m.readySite(id)
	if err != nil {
		return PerfView{}, err
	}
	v := PerfView{CacheDays: CacheDays, Headers: s.Headers, HeaderPresets: HeaderPresets}
	if v.Headers == nil {
		v.Headers = []HeaderRule{}
	}
	if s.Optimize != nil {
		v.Optimize = *s.Optimize
	}
	if phpApp(s) {
		st, err := m.PHPStatus(id)
		if err != nil {
			return PerfView{}, err
		}
		v.PHP = &st
	}
	for _, j := range m.jobs.List() {
		if (j.Kind == "php" || j.Kind == "optimize") && j.Site == id && (v.Job == nil || j.Started.After(v.Job.Started)) {
			jv := j
			v.Job = &jv
		}
	}
	return v, nil
}

// PHPStatus is the PHP state of a website.
func (m *Manager) PHPStatus(id string) (PHPState, error) {
	s, err := m.readySite(id)
	if err != nil {
		return PHPState{}, err
	}
	if !phpApp(s) {
		return PHPState{}, fmt.Errorf("%w: this website does not run PHP", ErrBadInput)
	}
	st := PHPState{Current: s.PHP, Previous: s.PrevPHP, Choices: []PHPChoice{}}
	for _, p := range catalog.All() {
		st.Choices = append(st.Choices, PHPChoice{Version: p.Version, EOL: p.EOL, Why: m.phpWhy(s, p.Version)})
	}
	for _, v := range m.jobs.List() {
		if v.Site == id && v.Kind == "php" && v.State == "running" {
			st.Running = true
		}
	}
	return st, nil
}

// StartPHPChange moves a website to PHP v: backup, rewrite, restart; the previous files come back on failure.
func (m *Manager) StartPHPChange(id, v string) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !phpApp(s) {
		return "", fmt.Errorf("%w: this website does not run PHP", ErrBadInput)
	}
	if _, ok := catalog.PHP(v); !ok {
		return "", fmt.Errorf("%w: PHP %q is not offered", ErrBadInput, v)
	}
	if v == s.PHP {
		return "", fmt.Errorf("%w: the website already runs PHP %s", ErrBadInput, v)
	}
	if why := m.phpWhy(s, v); why != "" {
		return "", fmt.Errorf("%w: %s", ErrBadInput, why)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("php", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		if err := m.changePHP(ctx, j, s, v); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

func (m *Manager) changePHP(ctx context.Context, j *Job, s Site, v string) error {
	next := s
	next.PHP, next.PrevPHP = v, s.PHP
	if s.IsNative() {
		name, err := m.backup(ctx, j, s, "before-php")
		if err != nil {
			return fmt.Errorf("the backup failed, nothing was changed: %v", err)
		}
		j.Step("restart PHP")
		if err := m.nativeRewrite(ctx, next, "PHP "+v, "PHP "+s.PHP); err != nil {
			return err
		}
		j.Step("done " + name)
		return nil
	}
	cfOld, err := s.Compose()
	if err != nil {
		return fmt.Errorf("%w: this agent no longer offers the version this website runs", ErrBadInput)
	}
	cfNew, err := next.Compose()
	if err != nil {
		return err
	}
	// only the services whose image follows PHP move; the others keep what they run (newer ones: "Update image")
	var follow []string
	was := imageMap(cfOld)
	for _, x := range composeImages(cfNew) {
		if was[x.service] != x.image {
			follow = append(follow, x.service)
		}
	}
	dir := m.dir(s.ID)
	disk, err := os.ReadFile(m.composePath(s.ID))
	if err != nil {
		return err
	}
	cf := keepImages(cfNew, string(disk), follow...)
	// 1. pull the new images while the website keeps running on the old ones
	j.Step("pull new images")
	if next.IonCube { // PHP-02: the loader of the new version sits next to the old one until the change is done
		if err := m.ionInstall(ctx, s.ID, v); err != nil {
			return fmt.Errorf("nothing was changed: %w", err)
		}
	}
	seen := map[string]bool{}
	for _, x := range composeImages(cf) {
		if !slices.Contains(follow, x.service) || strings.HasPrefix(x.image, localImage) || seen[x.image] {
			continue
		}
		seen[x.image] = true
		if out, err := m.run.Docker(ctx, dir, nil, "image", "inspect", "--format", "{{.Id}}", x.image); err == nil &&
			strings.HasPrefix(strings.TrimSpace(string(out)), "sha256:") {
			continue
		}
		if out, err := m.run.Docker(ctx, dir, nil, "pull", "--quiet", x.image); err != nil {
			return fmt.Errorf("pull %s failed, nothing was changed: %s", x.image, tail(out))
		}
	}
	// 2. a backup that can be restored if the website misbehaves on the new PHP
	name, err := m.backup(ctx, j, s, "before-php")
	if err != nil {
		return fmt.Errorf("the backup failed, nothing was changed: %v", err)
	}
	// 3. the new files, then build and recreate
	oldSite, err := os.ReadFile(filepath.Join(dir, "site.json"))
	if err != nil {
		return err
	}
	oldRecipe, recErr := os.ReadFile(m.recipePath(s.ID))
	putBack := func(why string) error {
		_ = writeFile(m.composePath(s.ID), disk, 0o644)
		_ = writeFile(filepath.Join(dir, "site.json"), oldSite, 0o644)
		if recErr == nil {
			_ = writeFile(m.recipePath(s.ID), oldRecipe, 0o644)
			if out, err := m.compose(ctx, s.ID, nil, "build", "--quiet", buildService(s)); err != nil {
				return fmt.Errorf("%s; the image of PHP %s did not build again either (%s): restore the backup %s", why, s.PHP, tail(out), name)
			}
		}
		j.Step("put back the previous PHP")
		if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
			return fmt.Errorf("%s; PHP %s did not start either (%s): restore the backup %s", why, s.PHP, tail(out), name)
		}
		return fmt.Errorf("%s; the website runs on PHP %s again", why, s.PHP)
	}
	if err := writeFile(m.composePath(s.ID), []byte(cf), 0o644); err != nil {
		return putBack(err.Error())
	}
	if err := writeSite(dir, next); err != nil {
		return putBack(err.Error())
	}
	if rec := buildRecipe(next); rec != "" {
		if err := os.MkdirAll(filepath.Dir(m.recipePath(s.ID)), 0o755); err != nil {
			return putBack(err.Error())
		}
		if err := writeFile(m.recipePath(s.ID), []byte(rec), 0o644); err != nil {
			return putBack(err.Error())
		}
		j.Step("build image")
		if out, err := m.compose(ctx, s.ID, nil, "build", "--quiet", buildService(next)); err != nil {
			return putBack("the image build failed: " + tail(out))
		}
	}
	j.Step("recreate containers")
	if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return putBack(fmt.Sprintf("the website did not start on PHP %s: %s", v, tail(out)))
	}
	if next.IonCube {
		m.ionTidy(s.ID, v)
	}
	j.Step("done " + name)
	return nil
}
