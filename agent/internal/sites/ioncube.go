package sites

// PHP-02: the ionCube Loader per website, for PHP applications sold encoded with ionCube.
//
// The loader is a closed-source PHP extension, free to use, published by ionCube as one archive per CPU type
// (every PHP version inside). The agent downloads that archive over HTTPS from ionCube itself (it is never part of
// this repository or an image), keeps the non-thread-safe loaders in <data>/cache/ioncube/ with the archive's
// sha256, and copies the loader of the website's PHP version into the website folder (ioncube/). Two read-only
// mounts load it in each PHP container: the loader and 00-ioncube.ini (zend_extension, first in conf.d). The official
// images stay as they are, so nothing is built. Docker websites only: the native engine's PHP-FPM is shared by every
// website of a PHP version.

import (
	"archive/tar"
	"compress/gzip"
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
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const (
	ionDir      = "ioncube"
	ionIni      = "00-ioncube.ini"
	ionInPHP    = "/usr/local/lib/nostek-ioncube.so"
	ionMaxBytes = 96 << 20
	ionMaxAge   = 30 * 24 * time.Hour // an older cache is fetched again before it is used for a website
)

// ionArchive is the loader archive of this server's CPU type.
func ionArchive() string {
	arch := "x86-64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	return "https://downloads.ioncube.com/loader_downloads/ioncube_loaders_lin_" + arch + ".tar.gz"
}

var ionLoaderRe = regexp.MustCompile(`^ioncube/ioncube_loader_lin_([0-9]+\.[0-9]+)\.so$`) // _ts.so is skipped

// ionFile is the loader of a PHP version inside the cache and the website folder.
func ionFile(v string) string { return "ioncube_loader_lin_" + v + ".so" }

// IonCubeCache is what the agent keeps of the loader archive.
type IonCubeCache struct {
	Fetched  int64    `json:"fetched"`
	SHA256   string   `json:"sha256"` // of the archive
	URL      string   `json:"url"`
	Versions []string `json:"versions"` // PHP versions it has a loader for
}

func (m *Manager) ionCacheDir() string { return filepath.Join(m.cfg.Data, "cache", "ioncube") }

func (m *Manager) ionCache() (IonCubeCache, bool) {
	var c IonCubeCache
	b, err := os.ReadFile(filepath.Join(m.ionCacheDir(), "cache.json"))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return IonCubeCache{}, false
	}
	return c, true
}

// fetchIonCube downloads the archive and replaces the cache with its loaders.
func (m *Manager) fetchIonCube(ctx context.Context) (IonCubeCache, error) {
	url := m.ionURL
	if url == "" {
		url = ionArchive()
	}
	hc := m.ionHTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return IonCubeCache{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return IonCubeCache{}, fmt.Errorf("the ionCube Loader could not be downloaded: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return IonCubeCache{}, fmt.Errorf("the ionCube Loader could not be downloaded: %s answered %d", url, resp.StatusCode)
	}
	tmp := m.ionCacheDir() + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return IonCubeCache{}, err
	}
	defer os.RemoveAll(tmp)
	h := sha256.New()
	lr := &io.LimitedReader{R: resp.Body, N: ionMaxBytes + 1}
	body := io.TeeReader(lr, h)
	zr, err := gzip.NewReader(body)
	if err != nil {
		return IonCubeCache{}, fmt.Errorf("the ionCube archive cannot be read: %v", err)
	}
	c := IonCubeCache{URL: url, Fetched: m.now().Unix()}
	tr := tar.NewReader(zr)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return IonCubeCache{}, fmt.Errorf("the ionCube archive cannot be read: %v", err)
		}
		mm := ionLoaderRe.FindStringSubmatch(hd.Name)
		if mm == nil || hd.Typeflag != tar.TypeReg || hd.Size > 32<<20 {
			continue
		}
		f, err := os.OpenFile(filepath.Join(tmp, ionFile(mm[1])), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return IonCubeCache{}, err
		}
		_, err = io.Copy(f, io.LimitReader(tr, hd.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return IonCubeCache{}, err
		}
		c.Versions = append(c.Versions, mm[1])
	}
	if _, err := io.Copy(io.Discard, body); err != nil { // the whole archive counts for its sha256
		return IonCubeCache{}, fmt.Errorf("the ionCube Loader could not be downloaded: %v", err)
	}
	if lr.N <= 0 {
		return IonCubeCache{}, errors.New("the ionCube archive is larger than expected")
	}
	if len(c.Versions) == 0 {
		return IonCubeCache{}, errors.New("the ionCube archive holds no loader for Linux")
	}
	sort.Slice(c.Versions, func(i, j int) bool { return spec.PHPLess(c.Versions[i], c.Versions[j]) })
	c.SHA256 = hex.EncodeToString(h.Sum(nil))
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "cache.json"), b, 0o644); err != nil {
		return IonCubeCache{}, err
	}
	_ = os.RemoveAll(m.ionCacheDir())
	if err := os.MkdirAll(filepath.Dir(m.ionCacheDir()), 0o755); err != nil {
		return IonCubeCache{}, err
	}
	if err := os.Rename(tmp, m.ionCacheDir()); err != nil {
		return IonCubeCache{}, err
	}
	return c, nil
}

// ionLoader returns the cache with a loader for PHP v, downloading the archive when there is none or it is old.
func (m *Manager) ionLoader(ctx context.Context, v string) (IonCubeCache, error) {
	m.ionMu.Lock()
	defer m.ionMu.Unlock()
	c, ok := m.ionCache()
	if ok && slices.Contains(c.Versions, v) && m.now().Sub(time.Unix(c.Fetched, 0)) < ionMaxAge {
		return c, nil
	}
	nc, err := m.fetchIonCube(ctx)
	if err != nil {
		if ok && slices.Contains(c.Versions, v) { // the old loader still works
			return c, nil
		}
		return IonCubeCache{}, err
	}
	if !slices.Contains(nc.Versions, v) {
		return nc, fmt.Errorf("%w: ionCube has no loader for PHP %s yet (it has %s)", ErrBadInput, v, strings.Join(nc.Versions, ", "))
	}
	return nc, nil
}

// ionInstall copies the loader of PHP v and its ini into the website folder.
func (m *Manager) ionInstall(ctx context.Context, id, v string) error {
	if _, err := m.ionLoader(ctx, v); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(m.ionCacheDir(), ionFile(v)))
	if err != nil {
		return err
	}
	d := filepath.Join(m.dir(id), ionDir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(d, ionFile(v)), b, 0o644); err != nil {
		return err
	}
	return writeFile(filepath.Join(d, ionIni), []byte("; Generated by nostek-agent (PHP-02). Do not edit: the agent rewrites it.\nzend_extension="+ionInPHP+"\n"), 0o644)
}

// ionTidy removes the loaders of other PHP versions from the website folder.
func (m *Manager) ionTidy(id, v string) {
	d := filepath.Join(m.dir(id), ionDir)
	ents, _ := os.ReadDir(d)
	for _, e := range ents {
		if e.Name() != ionFile(v) && e.Name() != ionIni {
			_ = os.Remove(filepath.Join(d, e.Name()))
		}
	}
}

// ionCubeMounts adds the loader and its ini next to the php.ini mount of every PHP service.
func (s Site) ionCubeMounts(cf string) string {
	if !s.IonCube {
		return cf
	}
	const at = "      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro"
	add := at + "\n      - ./" + ionDir + "/" + ionFile(s.PHP) + ":" + ionInPHP + ":ro\n      - ./" + ionDir + "/" + ionIni + ":/usr/local/etc/php/conf.d/" + ionIni + ":ro"
	return strings.ReplaceAll(cf, at, add)
}

// ionCubeApp tells whether a website can load the ionCube Loader.
func ionCubeApp(s Site) bool { return phpApp(s) && !s.IsNative() }

// IonCubeView is the ionCube card of a website.
type IonCubeView struct {
	Possible bool          `json:"possible"`
	Why      string        `json:"why,omitempty"`
	On       bool          `json:"on"`
	PHP      string        `json:"php"`
	Loaded   string        `json:"loaded,omitempty"` // the loader's line of php -v while on
	Cache    *IonCubeCache `json:"cache,omitempty"`
	Job      *JobView      `json:"job,omitempty"`
}

// IonCube returns the ionCube card of a website.
func (m *Manager) IonCube(ctx context.Context, id string) (IonCubeView, error) {
	s, err := m.readySite(id)
	if err != nil {
		return IonCubeView{}, err
	}
	v := IonCubeView{Possible: ionCubeApp(s), On: s.IonCube, PHP: s.PHP}
	switch {
	case s.IsNative():
		v.Why = "the native engine shares PHP between websites; ionCube is for Docker websites"
	case !phpApp(s):
		v.Why = "this website does not run PHP"
	}
	if c, ok := m.ionCache(); ok {
		v.Cache = &c
	}
	for _, j := range m.jobs.List() {
		if j.Kind == "ioncube" && j.Site == id && (v.Job == nil || j.Started.After(v.Job.Started)) {
			jv := j
			v.Job = &jv
		}
	}
	if s.IonCube {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		v.Loaded, _ = m.ionLoaded(ctx, s)
	}
	return v, nil
}

// ionLoaded returns the ionCube line of php -v in the website's PHP container, or "".
func (m *Manager) ionLoaded(ctx context.Context, s Site) (string, error) {
	cf, err := os.ReadFile(m.composePath(s.ID))
	if err != nil {
		return "", err
	}
	svcs := make([]string, 0, 2)
	for k := range phpServices(string(cf)) {
		svcs = append(svcs, k)
	}
	if len(svcs) == 0 {
		return "", errors.New("no PHP service")
	}
	sort.Strings(svcs)
	out, err := m.compose(ctx, s.ID, nil, "exec", "-T", svcs[0], "php", "-v")
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, tail(out))
	}
	for l := range strings.SplitSeq(string(out), "\n") {
		if strings.Contains(l, "ionCube") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "with ")), nil
		}
	}
	return "", nil
}

// StartIonCube turns the ionCube Loader of a website on or off (a job: the website restarts).
func (m *Manager) StartIonCube(id string, on bool) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !ionCubeApp(s) {
		return "", fmt.Errorf("%w: ionCube is for Docker websites that run PHP", ErrBadInput)
	}
	if s.IonCube == on {
		return "", fmt.Errorf("%w: nothing changes", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("ioncube", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := m.ionCubeChange(ctx, j, s, on); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

func (m *Manager) ionCubeChange(ctx context.Context, j *Job, s Site, on bool) error {
	next := s
	next.IonCube = on
	if on {
		j.Step("loader")
		if err := m.ionInstall(ctx, s.ID, s.PHP); err != nil {
			return fmt.Errorf("nothing was changed: %w", err)
		}
	}
	j.Step("restart website")
	what := "with the ionCube Loader"
	if !on {
		what = "without the ionCube Loader"
	}
	if err := m.swapStack(ctx, s, next, what); err != nil {
		return err
	}
	if !on {
		_ = os.RemoveAll(filepath.Join(m.dir(s.ID), ionDir))
		return nil
	}
	j.Step("check")
	line, err := m.ionLoaded(ctx, next)
	if err == nil && line == "" {
		err = errors.New("php -v does not list it")
	}
	if err != nil {
		if err2 := m.swapStack(ctx, next, s, "without the ionCube Loader"); err2 != nil {
			return fmt.Errorf("PHP did not load the ionCube Loader (%v), and turning it off again failed: %v", err, err2)
		}
		_ = os.RemoveAll(filepath.Join(m.dir(s.ID), ionDir))
		return fmt.Errorf("PHP did not load the ionCube Loader (%v); the website runs without it again", err)
	}
	return nil
}
