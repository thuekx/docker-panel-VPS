package sites

// Static releases (SITE-03). A static website (HTML, a built React/Vue/Svelte/Astro site) can be published from a
// .zip: the zip is unpacked outside the live folder, checked (an index.html at its top, or inside its one top
// folder), then swapped in with renames so visitors never see a half-written site. The live release and the two
// before it are kept on disk; any kept one can be put back with one click. The website's own error pages and
// .well-known stay in place across every swap.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// MaxRelease limits one uploaded release zip.
	MaxRelease      = 512 << 20
	keepStaticOld   = KeepReleases - 1 // release folders kept besides the live one
	keepStaticHist  = 30
	staticHistory   = "history.json"
	releaseNameMax  = 120
	releaseFolderRx = ".new-"
)

// ErrNotStatic is returned for a release request on a website that is not a static one.
var ErrNotStatic = errors.New("only a static website is published from a .zip")

// StaticRelease is one entry of a static website's release history.
type StaticRelease struct {
	Release  int    `json:"release"`
	At       int64  `json:"at"`
	Name     string `json:"name,omitempty"` // the uploaded file's name; "" for what was there before the first upload
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Result   string `json:"result"` // live | ok (kept on disk) | removed
	Rollback bool   `json:"rollback,omitempty"`
}

func (m *Manager) staticDir(id string) string { return filepath.Join(m.dir(id), "static") }
func (m *Manager) staticRel(id string, n int) string {
	return filepath.Join(m.staticDir(id), "releases", strconv.Itoa(n))
}

func (m *Manager) staticSite(id string) (Site, error) {
	s, err := m.Get(id)
	if err != nil {
		return s, err
	}
	if s.AppName() != "static" {
		return s, ErrNotStatic
	}
	return s, nil
}

// Releases returns the release history of a static website, newest first.
func (m *Manager) Releases(id string) ([]StaticRelease, error) {
	if _, err := m.staticSite(id); err != nil {
		return nil, err
	}
	return m.readReleases(id)
}

func (m *Manager) readReleases(id string) ([]StaticRelease, error) {
	b, err := os.ReadFile(filepath.Join(m.staticDir(id), staticHistory))
	if errors.Is(err, fs.ErrNotExist) {
		return []StaticRelease{}, nil
	}
	if err != nil {
		return nil, err
	}
	var h []StaticRelease
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	sort.Slice(h, func(i, j int) bool { return h[i].Release > h[j].Release })
	return h, nil
}

func (m *Manager) writeReleases(id string, h []StaticRelease) error {
	sort.Slice(h, func(i, j int) bool { return h[i].Release > h[j].Release })
	if len(h) > keepStaticHist {
		h = h[:keepStaticHist]
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(m.staticDir(id), staticHistory), b, 0o600)
}

// keepInPlace are the entries of the live folder that belong to the website, not to a release.
var keepInPlace = map[string]bool{errDir: true, ".well-known": true}

// cleanReleaseName keeps a short, printable file name for the history.
func cleanReleaseName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, `\`, "/"))
	var b strings.Builder
	for _, r := range n {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
	}
	n = strings.TrimSpace(b.String())
	if len(n) > releaseNameMax {
		n = n[:releaseNameMax]
	}
	if n == "." || n == "/" {
		n = ""
	}
	return n
}

// PublishStatic unpacks an uploaded zip into a new release and makes it live.
func (m *Manager) PublishStatic(id, name string, body io.Reader) (StaticRelease, error) {
	if _, err := m.staticSite(id); err != nil {
		return StaticRelease{}, err
	}
	if !m.mu.TryLock() {
		return StaticRelease{}, ErrBusy
	}
	defer m.mu.Unlock()
	dir := m.staticDir(id)
	if err := os.MkdirAll(filepath.Join(dir, "releases"), 0o700); err != nil {
		return StaticRelease{}, err
	}
	// a release folder left by an earlier failure
	if old, _ := filepath.Glob(filepath.Join(dir, "releases", releaseFolderRx+"*")); len(old) > 0 {
		for _, o := range old {
			_ = os.RemoveAll(o)
		}
	}
	tmp := filepath.Join(dir, "upload-"+randHex(4)+".zip")
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return StaticRelease{}, err
	}
	defer os.Remove(tmp)
	defer f.Close()
	size, err := io.Copy(f, &diskGuard{r: io.LimitReader(body, MaxRelease+1), dir: dir})
	if err != nil {
		return StaticRelease{}, err
	}
	if size > MaxRelease {
		return StaticRelease{}, ErrTooBig
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return StaticRelease{}, fmt.Errorf("%w: not a valid zip file", ErrBadInput)
	}
	hist, err := m.readReleases(id)
	if err != nil {
		return StaticRelease{}, err
	}
	n := 1
	for _, h := range hist {
		if h.Release >= n {
			n = h.Release + 1
		}
	}
	work := filepath.Join(dir, "releases", releaseFolderRx+strconv.Itoa(n))
	if err := os.Mkdir(work, 0o755); err != nil {
		return StaticRelease{}, err
	}
	defer os.RemoveAll(work)
	uid, gid := m.owner(id)
	r, err := os.OpenRoot(work)
	if err != nil {
		return StaticRelease{}, err
	}
	files, err := extractZip(r, ".", zr, uid, gid)
	r.Close()
	if err != nil {
		return StaticRelease{}, err
	}
	top, err := releaseTop(work)
	if err != nil {
		return StaticRelease{}, err
	}
	for k := range keepInPlace { // the website's own entries win over a release's
		_ = os.RemoveAll(filepath.Join(top, k))
	}
	live := 0
	for i := range hist {
		if hist[i].Result == "live" {
			live = hist[i].Release
		}
	}
	if live == 0 && len(hist) == 0 && !emptyLive(m.wpDir(id)) {
		// what was there before the first upload is kept as release 0, so it can be put back
		hist = append(hist, StaticRelease{Release: 0, At: m.now().Unix(), Result: "live"})
	}
	if err := m.swapStatic(id, live, top); err != nil {
		return StaticRelease{}, err
	}
	if len(hist) == 0 {
		_ = os.Remove(m.staticRel(id, live)) // nothing was live: no folder to keep
	}
	for i := range hist {
		if hist[i].Result == "live" {
			hist[i].Result = "ok"
		}
	}
	rel := StaticRelease{Release: n, At: m.now().Unix(), Name: cleanReleaseName(name), Files: files, Bytes: size, Result: "live"}
	hist = append(hist, rel)
	hist = m.pruneStatic(id, hist)
	return rel, m.writeReleases(id, hist)
}

// RollbackStatic puts a kept release back live.
func (m *Manager) RollbackStatic(id string, n int) (StaticRelease, error) {
	if _, err := m.staticSite(id); err != nil {
		return StaticRelease{}, err
	}
	if !m.mu.TryLock() {
		return StaticRelease{}, ErrBusy
	}
	defer m.mu.Unlock()
	hist, err := m.readReleases(id)
	if err != nil {
		return StaticRelease{}, err
	}
	live, at := -1, -1
	for i, h := range hist {
		if h.Result == "live" {
			live = h.Release
		}
		if h.Release == n {
			at = i
		}
	}
	if at < 0 || hist[at].Result != "ok" {
		return StaticRelease{}, fmt.Errorf("%w: release %d is not kept on disk", ErrBadInput, n)
	}
	src := m.staticRel(id, n)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return StaticRelease{}, fmt.Errorf("%w: release %d is not kept on disk", ErrBadInput, n)
	}
	if live < 0 {
		live = 0
	}
	if err := m.swapStatic(id, live, src); err != nil {
		return StaticRelease{}, err
	}
	_ = os.Remove(src)
	for i := range hist {
		if hist[i].Result == "live" {
			hist[i].Result = "ok"
		}
	}
	hist[at].Result, hist[at].Rollback, hist[at].At = "live", true, m.now().Unix()
	hist = m.pruneStatic(id, hist)
	return hist[at], m.writeReleases(id, hist)
}

// releaseTop is the folder of an unpacked release that holds index.html: its top, or its one top folder.
func releaseTop(work string) (string, error) {
	if fi, err := os.Lstat(filepath.Join(work, "index.html")); err == nil && fi.Mode().IsRegular() {
		return work, nil
	}
	ents, err := os.ReadDir(work)
	if err != nil {
		return "", err
	}
	var dirs []os.DirEntry
	for _, e := range ents {
		if e.Name() == "__MACOSX" || e.Name() == ".DS_Store" {
			continue
		}
		dirs = append(dirs, e)
	}
	if len(dirs) == 1 && dirs[0].IsDir() {
		in := filepath.Join(work, dirs[0].Name())
		if fi, err := os.Lstat(filepath.Join(in, "index.html")); err == nil && fi.Mode().IsRegular() {
			return in, nil
		}
	}
	return "", fmt.Errorf("%w: the zip has no index.html at its top (or inside its one top folder)", ErrBadInput)
}

// emptyLive says whether the live folder holds nothing but the website's own entries.
func emptyLive(wp string) bool {
	ents, _ := os.ReadDir(wp)
	for _, e := range ents {
		if !keepInPlace[e.Name()] {
			return false
		}
	}
	return true
}

// swapStatic moves the live release's entries into its release folder and the entries of src into the live
// folder. Every step is a rename on the same disk; a failure puts back what was moved.
func (m *Manager) swapStatic(id string, live int, src string) error {
	wp := m.wpDir(id)
	old := m.staticRel(id, live)
	_ = os.RemoveAll(old)
	if err := os.MkdirAll(old, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(wp)
	if err != nil {
		return err
	}
	var out []string
	undo := func() {
		for _, n := range out {
			_ = os.Rename(filepath.Join(old, n), filepath.Join(wp, n))
		}
	}
	for _, e := range ents {
		if keepInPlace[e.Name()] {
			continue
		}
		if err := os.Rename(filepath.Join(wp, e.Name()), filepath.Join(old, e.Name())); err != nil {
			undo()
			return err
		}
		out = append(out, e.Name())
	}
	ins, err := os.ReadDir(src)
	if err != nil {
		undo()
		return err
	}
	var in []string
	for _, e := range ins {
		if keepInPlace[e.Name()] {
			continue
		}
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(wp, e.Name())); err != nil {
			for _, n := range in {
				_ = os.Rename(filepath.Join(wp, n), filepath.Join(src, n))
			}
			undo()
			return err
		}
		in = append(in, e.Name())
	}
	return nil
}

// pruneStatic keeps the live release and the newest kept ones on disk; the rest are removed.
func (m *Manager) pruneStatic(id string, hist []StaticRelease) []StaticRelease {
	sort.Slice(hist, func(i, j int) bool { return hist[i].Release > hist[j].Release })
	kept := 0
	for i := range hist {
		if hist[i].Result != "ok" {
			continue
		}
		if kept < keepStaticOld {
			kept++
			continue
		}
		_ = os.RemoveAll(m.staticRel(id, hist[i].Release))
		hist[i].Result = "removed"
	}
	return hist
}
