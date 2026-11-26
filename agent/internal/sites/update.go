package sites

// UPD-01: update the agent and the panel from the panel. The agent lists the releases (GitHub, cached), finds the
// commit a tag names (git ls-remote), optionally makes a server backup first, and then starts the updater of the
// release that runs now (packages/upd-01/nostek-update.sh, checked against its sha256 lock file) as its own systemd
// unit, so restarting the agent or the panel does not stop it. The updater fetches the new release by commit,
// checks its scripts, runs SITE-01 APPLY, PANEL-01 UPDATE and both VERIFY, and puts the previous release back on
// any failure. Its status file and progress lines are read back here.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/npbk"
)

const (
	updateRepo  = "https://github.com/thuekx/docker-panel-VPS.git"
	updateAPI   = "https://api.github.com/repos/thuekx/docker-panel-VPS/releases?per_page=20"
	updateCache = 10 * time.Minute
	// UpdateCopy is the panel's copy of its database for a roll-back, written next to it just before an update.
	UpdateCopy = "update.db"
	// maxNotes caps one release's notes.
	maxNotes = 16 << 10
)

var tagRe = regexp.MustCompile(`^v(\d{1,4})\.(\d{1,4})\.(\d{1,4})$`)

// Release is one published release.
type Release struct {
	Tag       string    `json:"tag"`
	Name      string    `json:"name"`
	Notes     string    `json:"notes"`
	Published time.Time `json:"published"`
	Newer     bool      `json:"newer"`
}

// UpdateStatus is the updater's last (or current) run.
type UpdateStatus struct {
	Phase   string   `json:"phase"` // STARTING | RUNNING | DONE | FAILED | ROLLED_BACK | REFUSED
	Tag     string   `json:"tag"`
	From    string   `json:"from"`
	Step    string   `json:"step"`
	Result  string   `json:"result"`
	Started int64    `json:"started"`
	Ended   int64    `json:"ended"`
	Running bool     `json:"running"`
	Lines   []string `json:"lines"`
}

// UpdateView is what the panel's update page shows.
type UpdateView struct {
	Current  string        `json:"current"`
	Releases []Release     `json:"releases"`
	Error    string        `json:"error,omitempty"` // the release list could not be read
	Ready    string        `json:"ready,omitempty"` // why an update cannot start from the panel ("" = it can)
	Status   *UpdateStatus `json:"status,omitempty"`
	Job      *JobView      `json:"job,omitempty"` // the backup-and-start job
}

type updater struct {
	mu     sync.Mutex
	at     time.Time
	rel    []Release
	err    string
	job    string
	api    string
	repo   string
	varLib string
	host   HostRunner
	client *http.Client
}

func (m *Manager) updr() *updater {
	m.updOnce.Do(func() {
		if m.upd == nil {
			m.upd = &updater{api: updateAPI, repo: updateRepo, varLib: "/var/lib/nostek", host: ExecHost{},
				client: &http.Client{Timeout: 15 * time.Second}}
		}
	})
	return m.upd
}

// UseUpdateTest points the updater at test fixtures (tests only).
func (m *Manager) UseUpdateTest(api, repo, varLib string, h HostRunner) {
	m.upd = &updater{api: api, repo: repo, varLib: varLib, host: h, client: &http.Client{Timeout: 5 * time.Second}}
	m.updOnce.Do(func() {})
}

func verParse(t string) ([3]int, bool) {
	var v [3]int
	p := tagRe.FindStringSubmatch(t)
	if p == nil {
		return v, false
	}
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(p[i+1])
	}
	return v, true
}

// verNewer says whether release a is newer than b.
func verNewer(a, b string) bool {
	x, ok1 := verParse(a)
	y, ok2 := verParse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

// pkgState reads one key of a package's state file (PANEL-01, SITE-01).
func (u *updater) pkgState(pkg, key string) string {
	b, err := os.ReadFile(filepath.Join(u.varLib, pkg, "state"))
	if err != nil {
		return ""
	}
	v := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key+"=") {
			v = strings.TrimPrefix(l, key+"=")
		}
	}
	return v
}

func (u *updater) releases(ctx context.Context, fresh bool) ([]Release, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !fresh && !u.at.IsZero() && time.Since(u.at) < updateCache {
		return u.rel, u.err
	}
	rel, err := u.fetch(ctx)
	u.at = time.Now()
	if err != nil {
		u.err = err.Error()
		if u.rel != nil {
			return u.rel, u.err // keep the last good list
		}
		return nil, u.err
	}
	u.rel, u.err = rel, ""
	return rel, ""
}

func (u *updater) fetch(ctx context.Context) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "nostek-agent")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, errors.New("the release list could not be read from GitHub")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %d for the release list", resp.StatusCode)
	}
	var raw []struct {
		Tag        string    `json:"tag_name"`
		Name       string    `json:"name"`
		Body       string    `json:"body"`
		Published  time.Time `json:"published_at"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, errors.New("the release list from GitHub could not be read")
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || r.Prerelease || !tagRe.MatchString(r.Tag) {
			continue
		}
		notes := strings.ReplaceAll(r.Body, "\r\n", "\n")
		if len(notes) > maxNotes {
			notes = notes[:maxNotes] + "\n…"
		}
		name := r.Name
		if len(name) > 200 {
			name = name[:200]
		}
		out = append(out, Release{Tag: r.Tag, Name: name, Notes: notes, Published: r.Published})
	}
	// newest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && verNewer(out[j].Tag, out[j-1].Tag); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// script is the updater of the release that runs now; it must match its sha256 lock file.
func (m *Manager) updateScript() (string, error) {
	u := m.updr()
	c := u.pkgState("panel-01", "commit")
	if !commitRe.MatchString(c) || u.pkgState("panel-01", "phase") != "APPLIED" || u.pkgState("site-01", "phase") != "APPLIED" {
		return "", errors.New("the panel or the agent is not fully installed")
	}
	dir := filepath.Join(m.cfg.Data, "src", c, "packages", "upd-01")
	script := filepath.Join(dir, "nostek-update.sh")
	lock, err := os.ReadFile(filepath.Join(dir, "upd-01.lock"))
	if err != nil {
		return "", errors.New("the running release has no updater yet: update to it once with the runbook")
	}
	f := strings.Fields(string(lock))
	if len(f) < 2 || f[1] != "nostek-update.sh" {
		return "", errors.New("the updater's lock file is not readable")
	}
	b, err := os.ReadFile(script)
	if err != nil {
		return "", errors.New("the running release has no updater yet: update to it once with the runbook")
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != f[0] {
		return "", errors.New("the updater does not match its sha256")
	}
	return script, nil
}

func (u *updater) dir() string { return filepath.Join(u.varLib, "upd-01") }

// running says whether an updater holds its lock.
func (u *updater) running() bool {
	f, err := os.Open(filepath.Join(u.dir(), "lock"))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

func (u *updater) status() *UpdateStatus {
	b, err := os.ReadFile(filepath.Join(u.dir(), "status"))
	if err != nil {
		return nil
	}
	s := &UpdateStatus{}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch k {
		case "phase":
			s.Phase = v
		case "tag":
			s.Tag = v
		case "from":
			s.From = v
		case "step":
			s.Step = v
		case "result":
			s.Result = v
		case "started":
			s.Started, _ = strconv.ParseInt(v, 10, 64)
		case "ended":
			s.Ended, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if f, err := os.Open(filepath.Join(u.dir(), "progress")); err == nil {
		sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
		for sc.Scan() {
			s.Lines = append(s.Lines, sc.Text())
			if len(s.Lines) > 120 {
				s.Lines = s.Lines[1:]
			}
		}
		f.Close()
	}
	s.Running = u.running()
	return s
}

// UpdateInfo lists the releases and the last run. fresh skips the cache.
func (m *Manager) UpdateInfo(ctx context.Context, fresh bool) UpdateView {
	u := m.updr()
	v := UpdateView{Current: u.pkgState("panel-01", "release"), Status: u.status()}
	rel, e := u.releases(ctx, fresh)
	v.Error = e
	for _, r := range rel {
		r.Newer = verNewer(r.Tag, v.Current)
		v.Releases = append(v.Releases, r)
	}
	if _, err := m.updateScript(); err != nil {
		v.Ready = err.Error()
	}
	u.mu.Lock()
	id := u.job
	u.mu.Unlock()
	if j := m.jobs.Get(id); id != "" && j != nil {
		jv := j.View()
		v.Job = &jv
	}
	return v
}

// tagCommit asks the repository which commit a tag names (an annotated tag's commit is on its peeled line).
func (m *Manager) tagCommit(ctx context.Context, tag string) (string, error) {
	u := m.updr()
	ref := "refs/tags/" + tag
	out, err := u.host.Host(ctx, nil, "git", "ls-remote", "--tags", u.repo, ref)
	if err != nil {
		return "", errors.New("the repository could not be reached to find the release")
	}
	plain, peeled := "", ""
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || !commitRe.MatchString(f[0]) {
			continue
		}
		switch f[1] {
		case ref:
			plain = f[0]
		case ref + "^{}":
			peeled = f[0]
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	if plain != "" {
		return plain, nil
	}
	return "", fmt.Errorf("%w: release %s was not found in the repository", ErrBadInput, tag)
}

// StartUpdate starts an update to tag. With a passphrase, a server backup is made first; the update then starts
// only when the backup succeeded. It returns the job that makes the backup and starts the updater.
func (m *Manager) StartUpdate(ctx context.Context, tag, pass, panelVersion, agentVersion string) (string, error) {
	if !tagRe.MatchString(tag) {
		return "", fmt.Errorf("%w: unknown release", ErrBadInput)
	}
	u := m.updr()
	if u.running() {
		return "", fmt.Errorf("%w: an update is running", ErrBusy)
	}
	script, err := m.updateScript()
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrBadInput, err.Error())
	}
	cur := u.pkgState("panel-01", "release")
	rel, e := u.releases(ctx, false)
	found := false
	for _, r := range rel {
		found = found || r.Tag == tag
	}
	if !found {
		if e != "" {
			return "", fmt.Errorf("%w: %s", ErrBadInput, e)
		}
		return "", fmt.Errorf("%w: %s is not a published release", ErrBadInput, tag)
	}
	if !verNewer(tag, cur) {
		return "", fmt.Errorf("%w: %s is not newer than the running %s", ErrBadInput, tag, cur)
	}
	backupCopy := ""
	if pass != "" {
		if len([]rune(pass)) < npbk.MinPassphrase || len(pass) > 1024 {
			return "", fmt.Errorf("%w: the passphrase needs at least 12 characters", ErrBadInput)
		}
		if backupCopy, err = m.serverBackupReady(); err != nil {
			return "", err
		}
	}
	commit, err := m.tagCommit(ctx, tag)
	if err != nil {
		return "", err
	}
	if m.cfg.PreviewZone == "" {
		return "", fmt.Errorf("%w: the agent has no preview zone", ErrBadInput)
	}
	host, _ := os.Hostname()
	args := []string{"--unit", "nostek-update-" + strconv.FormatInt(m.now().Unix(), 10), "--description", "Nostek Panel update to " + tag,
		"--collect", "--quiet", "--", "/usr/bin/bash", script, "RUN", "--ack", "UPD-01", "--tag", tag, "--commit", commit,
		"--expect-ip", m.cfg.PublicIPv4, "--expect-host", host, "--domain", m.cfg.PanelDomain, "--preview-zone", m.cfg.PreviewZone}
	if cp := filepath.Join(m.panelDir(), UpdateCopy); fresh(cp, m.now()) {
		args = append(args, "--db-copy", cp)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("update", "")
	u.mu.Lock()
	u.job = j.ID
	u.mu.Unlock()
	go func() {
		defer m.mu.Unlock()
		if pass != "" {
			j.Step("backup")
			bctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
			name, err := m.serverBackup(bctx, j, pass, panelVersion, agentVersion)
			cancel()
			os.Remove(backupCopy)
			if err != nil {
				j.Fail("server backup failed, nothing was updated: " + err.Error())
				return
			}
			j.Step("done " + name)
		}
		j.Step("start")
		if err := u.writeStarting(tag, cur); err != nil {
			j.Fail(err.Error())
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := u.host.Host(sctx, nil, "systemd-run", args...); err != nil {
			msg := strings.TrimSpace(string(out))
			if len(msg) > 300 {
				msg = msg[:300]
			}
			_ = os.Remove(filepath.Join(u.dir(), "status"))
			j.Fail("the updater did not start: " + msg)
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

func fresh(p string, now time.Time) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular() && now.Sub(fi.ModTime()) < panelCopyAge
}

// writeStarting records the start, so the panel shows the update before the updater takes over its status file.
func (u *updater) writeStarting(tag, from string) error {
	if err := os.MkdirAll(u.dir(), 0o700); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(u.dir(), "progress"))
	body := fmt.Sprintf("phase=STARTING\ntag=%s\nfrom=%s\nstarted=%d\n", tag, from, time.Now().Unix())
	return os.WriteFile(filepath.Join(u.dir(), "status"), []byte(body), 0o600)
}
