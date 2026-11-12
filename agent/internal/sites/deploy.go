package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// APP-02: a Node.js website is built from its Git repository into a new release folder (wp/releases/<n>) by a
// throwaway container of the same official Node.js image, then the app container is switched to it. The live
// release keeps serving while the new one builds; when the new one does not start healthy, the previous release
// is put back. Deploy history and build logs live in deploy/ (outside the website folder).

// Deploy limits.
const (
	BuildTimeout  = 15 * time.Minute
	StartTimeout  = 180 // seconds the app may take to answer on its port
	KeepReleases  = 3   // release folders kept on disk (the live one included)
	KeepHistory   = 30
	MaxBuildLog   = 256 << 10
	buildMemory   = "1536m"
	buildCPUs     = "2"
	buildPIDs     = "1024"
	commitMarker  = "NOSTEK_COMMIT="
	deployHistory = "history.json"
)

// Deploy is one entry of a website's deploy history.
type Deploy struct {
	Release  int    `json:"release"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended,omitempty"`
	Commit   string `json:"commit,omitempty"`  // full commit id of the source
	Message  string `json:"message,omitempty"` // first line of the commit message
	Result   string `json:"result"`            // running | live | ok (was live, still on disk) | failed | removed
	Error    string `json:"error,omitempty"`
	Rollback bool   `json:"rollback,omitempty"` // this entry went live again by a rollback
}

func (m *Manager) deployDir(id string) string { return filepath.Join(m.dir(id), "deploy") }
func (m *Manager) releaseDir(id string, n int) string {
	return filepath.Join(m.dir(id), "wp", "releases", strconv.Itoa(n))
}

// History returns the deploy history of a Node.js website, newest first.
func (m *Manager) History(id string) ([]Deploy, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if !s.IsRuntime() {
		return nil, ErrNotNode
	}
	return m.readHistory(id)
}

// ErrNoLog is returned when a release has no build log.
var ErrNoLog = errors.New("no build log for this release")

// ErrNotNode is returned for a deploy request on a website that is not a Node.js app.
var ErrNotNode = errors.New("only a Node.js website is deployed from Git")

func (m *Manager) readHistory(id string) ([]Deploy, error) {
	b, err := os.ReadFile(filepath.Join(m.deployDir(id), deployHistory))
	if errors.Is(err, fs.ErrNotExist) {
		return []Deploy{}, nil
	}
	if err != nil {
		return nil, err
	}
	var h []Deploy
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	sort.Slice(h, func(i, j int) bool { return h[i].Release > h[j].Release })
	return h, nil
}

func (m *Manager) writeHistory(id string, h []Deploy) error {
	sort.Slice(h, func(i, j int) bool { return h[i].Release > h[j].Release })
	if len(h) > KeepHistory {
		h = h[:KeepHistory]
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(m.deployDir(id), deployHistory), b, 0o600)
}

// BuildLog returns the build log of one release.
func (m *Manager) BuildLog(id string, n int) (string, error) {
	s, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if !s.IsRuntime() {
		return "", ErrNotNode
	}
	if n < 1 {
		return "", ErrNoLog
	}
	b, err := os.ReadFile(filepath.Join(m.deployDir(id), "logs", strconv.Itoa(n)+".log"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoLog
	}
	return string(b), err
}

// StartDeploy builds the branch again into a new release and switches the website to it.
func (m *Manager) StartDeploy(id string) (string, error) {
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	s, err := m.Get(id)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	if !s.IsRuntime() {
		m.mu.Unlock()
		return "", ErrNotNode
	}
	if s.Phase != "ready" {
		m.mu.Unlock()
		return "", fmt.Errorf("%w: %s", ErrBadInput, "the website is not ready")
	}
	j := m.jobs.New("deploy", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), BuildTimeout+5*time.Minute)
		defer cancel()
		if err := m.deploy(ctx, j, s); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

// Rollback switches a Node.js website back to a release that is still on disk.
func (m *Manager) Rollback(ctx context.Context, id string, n int) error {
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	if !s.IsRuntime() {
		return ErrNotNode
	}
	if n == s.Release {
		return fmt.Errorf("%w: %s", ErrBadInput, "this release is already live")
	}
	h, err := m.readHistory(id)
	if err != nil {
		return err
	}
	i := findRelease(h, n)
	if i < 0 || (h[i].Result != "ok" && h[i].Result != "live") {
		return fmt.Errorf("%w: %s", ErrBadInput, "only a release that built and ran before, and is still kept, can go live again")
	}
	if _, err := os.Stat(m.releaseDir(id, n)); err != nil {
		return fmt.Errorf("%w: %s", ErrBadInput, "this release is no longer kept")
	}
	prev := s.Release
	if out, err := m.switchRelease(ctx, &s, n); err != nil {
		if prev > 0 {
			_, _ = m.switchRelease(ctx, &s, prev)
		}
		return fmt.Errorf("release %d did not start: %v %s", n, err, tail(out))
	}
	for k := range h {
		if h[k].Result == "live" {
			h[k].Result = "ok"
		}
	}
	h[i].Result, h[i].Rollback = "live", true
	return m.writeHistory(id, h)
}

func findRelease(h []Deploy, n int) int {
	for i := range h {
		if h[i].Release == n {
			return i
		}
	}
	return -1
}

// switchRelease points the app container at release n and waits until it is healthy.
func (m *Manager) switchRelease(ctx context.Context, s *Site, n int) ([]byte, error) {
	s.Release = n
	cf, err := s.Compose()
	if err != nil {
		return nil, err
	}
	dir := m.dir(s.ID)
	if err := writeFile(filepath.Join(dir, "compose.yml"), []byte(cf), 0o644); err != nil {
		return nil, err
	}
	if err := writeSite(dir, *s); err != nil {
		return nil, err
	}
	return m.compose(ctx, s.ID, nil, "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(StartTimeout), "app")
}

// Where the build container sees the deploy key and the pinned host keys of a private repository.
const (
	buildKeyPath    = "/run/nostek/deploy_key"
	buildKnownHosts = "/run/nostek/known_hosts"
)

// appFileCheck stops a build early when the app folder lacks the file its runtime needs.
var appFileCheck = map[string]string{
	spec.AppNode:   `[ -f package.json ] || { echo "package.json not found in the app folder"; exit 1; }`,
	spec.AppGo:     `[ -f go.mod ] || ls *.go >/dev/null 2>&1 || { echo "go.mod or .go files not found in the app folder"; exit 1; }`,
	spec.AppPython: `[ -f requirements.txt ] || [ -f pyproject.toml ] || ls *.py >/dev/null 2>&1 || { echo "requirements.txt or .py files not found in the app folder"; exit 1; }`,
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// deploy builds a new release and makes it live. The steps go to the job; the build output to the log file.
func (m *Manager) deploy(ctx context.Context, j *Job, s Site) error {
	n, _ := catalog.RuntimeOf(s.AppName(), s.Version)
	h, err := m.readHistory(s.ID)
	if err != nil {
		return err
	}
	next := 1
	for _, d := range h {
		if d.Release >= next {
			next = d.Release + 1
		}
	}
	d := Deploy{Release: next, Started: m.now().Unix(), Result: "running"}
	h = append(h, d)
	if err := m.writeHistory(s.ID, h); err != nil {
		return err
	}
	finish := func(res, msg string) {
		h, _ := m.readHistory(s.ID)
		if i := findRelease(h, d.Release); i >= 0 {
			if res == "live" {
				for k := range h {
					if h[k].Result == "live" {
						h[k].Result = "ok"
					}
				}
			}
			d.Result, d.Error, d.Ended = res, msg, m.now().Unix()
			h[i] = d
			_ = m.writeHistory(s.ID, h)
		}
		m.pruneReleases(s.ID)
	}

	j.Step("fetch and build")
	rel := m.releaseDir(s.ID, d.Release)
	logDir := filepath.Join(m.deployDir(s.ID), "logs")
	for _, p := range []string{rel, logDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			finish("failed", "files: "+err.Error())
			return err
		}
	}
	if err := os.Chown(rel, m.uid(), m.uid()); err != nil {
		finish("failed", "files: "+err.Error())
		return err
	}
	fetch := []string{
		`echo "== git clone $NOSTEK_BRANCH"`,
		`git clone --quiet --depth 1 --single-branch --branch "$NOSTEK_BRANCH" -- "$NOSTEK_REPO" "$HOME/src"`,
		`echo "` + commitMarker + `$(git -C "$HOME/src" rev-parse HEAD) $(git -C "$HOME/src" log -1 --format=%s | head -c 200)"`,
		`rm -rf "$HOME/src/.git"`,
		`src="$HOME/src"`,
	}
	if s.FromFiles() {
		fetch = []string{`echo "== copy the source folder"`, `src=/src`}
	}
	script := strings.Join(append(append([]string{"set -e"}, fetch...),
		`[ -d "$src/$NOSTEK_DIR" ] || { echo "app folder '$NOSTEK_DIR' not found"; exit 1; }`,
		`cp -R "$src/$NOSTEK_DIR/." /app/`,
		appFileCheck[s.AppName()],
		`echo "== install"`, spec.InstallCmd[s.AppName()],
		`echo "== build"`, s.BuildCmd(),
		`echo "== build done"`,
	), "\n")
	args := []string{"run", "--rm", "--name", fmt.Sprintf("np-build-%s-%d", s.ID, d.Release),
		"--user", fmt.Sprintf("%d:%d", m.uid(), m.uid()),
		"--memory", buildMemory, "--cpus", buildCPUs, "--pids-limit", buildPIDs,
		"--security-opt", "no-new-privileges:true", "--cap-drop", "ALL",
		"--label", "nostek.build=" + s.ID,
		"-e", "HOME=/tmp", "-e", "CI=1",
		"-e", "NPM_CONFIG_UPDATE_NOTIFIER=false", "-e", "NEXT_TELEMETRY_DISABLED=1",
		"-e", "GOCACHE=/tmp/go-cache", "-e", "GOPATH=/tmp/go", "-e", "GOFLAGS=-buildvcs=false",
		"-e", "PIP_DISABLE_PIP_VERSION_CHECK=1",
		"-e", "NOSTEK_DIR=" + s.Dir,
	}
	if s.FromFiles() {
		args = append(args, "-v", filepath.Join(m.dir(s.ID), "wp", "source")+":/src:ro")
	} else {
		args = append(args, "-e", "NOSTEK_REPO="+s.Repo, "-e", "NOSTEK_BRANCH="+s.Branch, "-e", "GIT_TERMINAL_PROMPT=0")
		if spec.IsSSHRepo(s.Repo) {
			// the deploy key read only; the host key is learnt on the first fetch and pinned from then on
			args = append(args, "-v", m.deployKeyPath(s.ID)+":"+buildKeyPath+":ro", "-v", m.knownHostsPath(s.ID)+":"+buildKnownHosts,
				"-e", "GIT_SSH_COMMAND=ssh -i "+buildKeyPath+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+buildKnownHosts+" -o ConnectTimeout=20")
		}
	}
	if envf := filepath.Join(m.deployDir(s.ID), "app.env"); fileExists(envf) {
		args = append(args, "--env-file", envf)
	}
	args = append(args, "-v", rel+":/app", "-w", "/app", n.Image, "sh", "-c", script)
	bctx, cancel := context.WithTimeout(ctx, BuildTimeout)
	out, berr := m.run.Docker(bctx, m.dir(s.ID), nil, args...)
	cancel()
	if len(out) > MaxBuildLog {
		out = append([]byte("... (start of the log cut)\n"), out[len(out)-MaxBuildLog:]...)
	}
	_ = writeFile(filepath.Join(logDir, strconv.Itoa(d.Release)+".log"), out, 0o600)
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, commitMarker); ok {
			c, msg, _ := strings.Cut(rest, " ")
			if commitRe.MatchString(c) {
				d.Commit, d.Message = c, strings.TrimSpace(msg)
			}
		}
	}
	if berr != nil {
		msg := "build failed: see the build log"
		if errors.Is(bctx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("build took longer than %d minutes", int(BuildTimeout.Minutes()))
		}
		finish("failed", msg)
		return errors.New(msg)
	}

	j.Step("start new release")
	prev := s.Release
	if sout, err := m.switchRelease(ctx, &s, d.Release); err != nil {
		msg := fmt.Sprintf("the app did not answer on port %d within %d seconds", NodePort, StartTimeout)
		_ = appendFile(filepath.Join(logDir, strconv.Itoa(d.Release)+".log"), "\n== start failed\n"+tail(sout)+"\n")
		if prev > 0 {
			j.Step("previous release back")
			_, _ = m.switchRelease(ctx, &s, prev)
		}
		finish("failed", msg)
		return errors.New(msg)
	}
	finish("live", "")
	return nil
}

// pruneReleases keeps the live release and the newest others up to KeepReleases folders; history entries of
// removed folders are marked "removed" (a failed one stays "failed").
func (m *Manager) pruneReleases(id string) {
	s, err := m.Get(id)
	if err != nil {
		return
	}
	h, err := m.readHistory(id)
	if err != nil {
		return
	}
	kept := 0
	for i := range h { // newest first
		n := h[i].Release
		if h[i].Result == "running" {
			continue
		}
		if n == s.Release || (kept < KeepReleases-1 && h[i].Result == "ok") {
			if n != s.Release {
				kept++
			}
			continue
		}
		if err := os.RemoveAll(m.releaseDir(id, n)); err == nil && h[i].Result == "ok" {
			h[i].Result = "removed"
		}
	}
	_ = m.writeHistory(id, h)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func appendFile(p, text string) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

// createNode makes a Node.js website: folders, the image, the first build and start. A failed first build
// leaves the website "failed" with its build log, like any failed create.
func (m *Manager) createNode(ctx context.Context, j *Job, s Site, fail func(string, error, []byte)) {
	dir := m.dir(s.ID)
	j.Step("files")
	for _, d := range []string{dir, filepath.Join(dir, "wp", "releases"), filepath.Join(m.deployDir(s.ID), "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := os.Chown(filepath.Join(dir, "wp"), m.uid(), m.uid()); err != nil {
		fail("files", err, nil)
		return
	}
	if s.FromFiles() {
		if err := m.writeAppStarter(s); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if spec.IsSSHRepo(s.Repo) {
		if err := m.adoptDeployKey(s, s.keyID); err != nil {
			fail("files", err, nil)
			return
		}
	}
	if err := writeSite(dir, s); err != nil {
		fail("files", err, nil)
		return
	}
	n, _ := catalog.RuntimeOf(s.AppName(), s.Version)
	j.Step("pull images")
	if out, err := m.run.Docker(ctx, dir, nil, "pull", "--quiet", n.Image); err != nil {
		fail("pull images", err, out)
		return
	}
	if err := m.deploy(ctx, j, s); err != nil {
		fail("deploy", err, nil)
		return
	}
	s, err := m.Get(s.ID)
	if err != nil {
		fail("finish", err, nil)
		return
	}
	s.Phase = "ready"
	if err := writeSite(dir, s); err != nil {
		fail("finish", err, nil)
		return
	}
	j.Done("")
}
