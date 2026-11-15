package sites

// Disk usage (DISK-01, owner 2026-11-16 01:51 WIB, like cPanel's Disk Usage). Per website: how much its files,
// database, backups and file trash take, and a drill-down of its files folder by size with its largest files. For
// the server: the disk, every website, the website trash, and what Docker keeps (images, build cache), with a safe
// clean-up of what no website uses. Reading only walks folders; it never follows a symbolic link.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Disk limits: a drill-down lists at most MaxDiskRows children and MaxBigFiles largest files; a walk stops
// after DiskWalkTime and says so (a huge folder never hangs the page).
const (
	MaxDiskRows  = 100
	MaxBigFiles  = 20
	DiskWalkTime = 20 * time.Second
)

// DiskRow is one folder or file of a drill-down with everything below it.
type DiskRow struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // dir | file | link
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}

// BigFile is one of the largest files of a website.
type BigFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	MTime int64  `json:"mtime"`
}

// SiteDisk is the disk usage of one website.
type SiteDisk struct {
	Files      int64     `json:"files"`      // wp/: the website's own files (what the File Manager shows)
	Database   int64     `json:"database"`   // db/: the database server's files
	Backups    int64     `json:"backups"`    // backups/
	Trash      int64     `json:"trash"`      // files-trash/: files deleted with the File Manager (7 days)
	Other      int64     `json:"other"`      // releases of an app, settings, logs
	Total      int64     `json:"total"`      // all of the website's folder
	Path       string    `json:"path"`       // the drill-down folder, as the File Manager writes it
	Rows       []DiskRow `json:"rows"`       // its children, largest first
	More       int       `json:"more"`       // children not listed
	Big        []BigFile `json:"big"`        // the largest files of the website's files
	Incomplete bool      `json:"incomplete"` // the walk stopped early: sizes are at least these
}

// walkSize adds every regular file below dir of root (never following a link).
func walkSize(ctx context.Context, fsys fs.FS, dir string, each func(p string, d fs.DirEntry, fi fs.FileInfo)) (int64, int, bool) {
	var total int64
	n, cut := 0, false
	_ = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skipped, the rest still counts
		}
		if ctx.Err() != nil {
			cut = true
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		total += fi.Size()
		n++
		if each != nil {
			each(p, d, fi)
		}
		return nil
	})
	return total, n, cut
}

func dirSize(ctx context.Context, dir string) (int64, bool) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return 0, false
	}
	defer r.Close()
	b, _, cut := walkSize(ctx, r.FS(), ".", nil)
	return b, cut
}

// Disk reports the disk usage of a website and the drill-down of p inside its files folder.
func (m *Manager) Disk(ctx context.Context, id, p string) (SiteDisk, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return SiteDisk{}, err
	}
	if _, err := m.Get(id); err != nil {
		return SiteDisk{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, DiskWalkTime)
	defer cancel()
	out := SiteDisk{Path: shown(rel), Rows: []DiskRow{}, Big: []BigFile{}}
	cut := false
	for _, part := range []struct {
		dir string
		to  *int64
	}{{"db", &out.Database}, {"backups", &out.Backups}, {"files-trash", &out.Trash}} {
		b, c := dirSize(ctx, filepath.Join(m.dir(id), part.dir))
		*part.to, cut = b, cut || c
	}
	total, c := dirSize(ctx, m.dir(id))
	out.Total, cut = total, cut || c

	r, err := m.openWP(id)
	if err != nil {
		return SiteDisk{}, err
	}
	defer r.Close()
	fsys := r.FS()
	fi, err := r.Lstat(rel)
	if err != nil {
		return SiteDisk{}, notExist(err)
	}
	if !fi.IsDir() {
		return SiteDisk{}, fmt.Errorf("%w: not a folder", ErrBadInput)
	}
	// the files of the website: their total and the largest ones
	var big []BigFile
	files, _, c := walkSize(ctx, fsys, ".", func(fp string, _ fs.DirEntry, fi fs.FileInfo) {
		big = append(big, BigFile{Path: shown(fp), Bytes: fi.Size(), MTime: fi.ModTime().Unix()})
		if len(big) > 4*MaxBigFiles { // keep the list short while walking
			sort.Slice(big, func(a, b int) bool { return big[a].Bytes > big[b].Bytes })
			big = big[:MaxBigFiles]
		}
	})
	out.Files, cut = files, cut || c
	sort.Slice(big, func(a, b int) bool { return big[a].Bytes > big[b].Bytes })
	if len(big) > MaxBigFiles {
		big = big[:MaxBigFiles]
	}
	out.Big = append(out.Big, big...)
	if o := out.Total - out.Files - out.Database - out.Backups - out.Trash; o > 0 {
		out.Other = o
	}
	// the drill-down: each child of rel with everything below it
	ents, err := fs.ReadDir(fsys, rel)
	if err != nil {
		return SiteDisk{}, notExist(err)
	}
	for _, e := range ents {
		row := DiskRow{Name: e.Name(), Type: "file"}
		child := path.Join(rel, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			row.Type = "link"
		case e.IsDir():
			row.Type = "dir"
			b, n, c := walkSize(ctx, fsys, child, nil)
			row.Bytes, row.Files, cut = b, n, cut || c
		default:
			if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
				row.Bytes, row.Files = fi.Size(), 1
			}
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(a, b int) bool {
		if out.Rows[a].Bytes != out.Rows[b].Bytes {
			return out.Rows[a].Bytes > out.Rows[b].Bytes
		}
		return out.Rows[a].Name < out.Rows[b].Name
	})
	if len(out.Rows) > MaxDiskRows {
		out.More, out.Rows = len(out.Rows)-MaxDiskRows, out.Rows[:MaxDiskRows]
	}
	out.Incomplete = cut
	return out, nil
}

// SiteTotal is one website on the server's disk view.
type SiteTotal struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Bytes  int64  `json:"bytes"`
}

// DockerUse is one kind of thing Docker keeps on the disk (docker system df).
type DockerUse struct {
	Kind        string `json:"kind"` // images | containers | volumes | build-cache
	Count       int    `json:"count"`
	Bytes       int64  `json:"bytes"`
	Reclaimable int64  `json:"reclaimable"`
}

// ServerDisk is the disk view of the server.
type ServerDisk struct {
	DiskBytes  int64       `json:"disk_bytes"` // the size of the disk that holds the websites
	UsedBytes  int64       `json:"used_bytes"`
	FreeBytes  int64       `json:"free_bytes"`
	Sites      []SiteTotal `json:"sites"` // largest first
	Trash      int64       `json:"trash"` // deleted websites kept 7 days
	Docker     []DockerUse `json:"docker"`
	DockerErr  string      `json:"docker_error,omitempty"`
	Incomplete bool        `json:"incomplete"`
}

// ServerDiskUsage reports the disk of the server, every website and Docker's images and build cache.
func (m *Manager) ServerDiskUsage(ctx context.Context) (ServerDisk, error) {
	ctx, cancel := context.WithTimeout(ctx, DiskWalkTime)
	defer cancel()
	out := ServerDisk{Sites: []SiteTotal{}, Docker: []DockerUse{}}
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.cfg.Data, &st); err == nil {
		bs := int64(st.Bsize)
		out.DiskBytes, out.FreeBytes = int64(st.Blocks)*bs, int64(st.Bavail)*bs
		out.UsedBytes = out.DiskBytes - int64(st.Bfree)*bs
	}
	all, err := m.List()
	if err != nil {
		return out, err
	}
	for _, s := range all {
		b, cut := dirSize(ctx, m.dir(s.ID))
		out.Incomplete = out.Incomplete || cut
		out.Sites = append(out.Sites, SiteTotal{ID: s.ID, Domain: s.Domain, Bytes: b})
	}
	sort.SliceStable(out.Sites, func(a, b int) bool { return out.Sites[a].Bytes > out.Sites[b].Bytes })
	tb, cut := dirSize(ctx, m.trashDir())
	out.Trash, out.Incomplete = tb, out.Incomplete || cut
	out.Docker, err = m.dockerDF(ctx)
	if err != nil {
		out.DockerErr = err.Error()
		out.Docker = []DockerUse{}
	}
	return out, nil
}

// dockerDF reads `docker system df` (sizes as Docker prints them, turned into bytes).
func (m *Manager) dockerDF(ctx context.Context) ([]DockerUse, error) {
	raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "system", "df", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker system df failed: %s", tail(raw))
	}
	kinds := map[string]string{"Images": "images", "Containers": "containers", "Local Volumes": "volumes", "Build Cache": "build-cache"}
	var out []DockerUse
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r struct {
			Type        string `json:"Type"`
			TotalCount  any    `json:"TotalCount"`
			Size        string `json:"Size"`
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(l), &r) != nil || kinds[r.Type] == "" {
			continue
		}
		n := 0
		switch v := r.TotalCount.(type) {
		case float64:
			n = int(v)
		case string:
			fmt.Sscan(v, &n)
		}
		rec, _, _ := strings.Cut(r.Reclaimable, " ") // "1.2GB (40%)"
		out = append(out, DockerUse{Kind: kinds[r.Type], Count: n, Bytes: max(parseSize(r.Size), 0), Reclaimable: max(parseSize(rec), 0)})
	}
	if len(out) == 0 {
		return nil, errors.New("docker system df gave nothing")
	}
	return out, nil
}

// Clean-ups the panel offers. Both leave every website and the panel as they are: an image a website's compose
// file names, an image of the panel itself (nostek/…), or one a container uses (running or stopped), is never removed.
const (
	CleanImages = "images"      // images no website names (old versions after Update image, leftovers)
	CleanBuild  = "build-cache" // the build cache of Laravel, PHP and app builds older than a day
)

type imageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Digest     string `json:"Digest"`
}

// keptImages are the images every website's compose file names (WP-CLI and build images too, which have no
// running container most of the time).
func (m *Manager) keptImages() (map[string]bool, error) {
	keep := map[string]bool{}
	all, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		cf, err := os.ReadFile(m.composePath(s.ID))
		if err != nil {
			continue
		}
		for _, x := range composeImages(string(cf)) {
			keep[x.image] = true
		}
	}
	return keep, nil
}

// platformImage is an image of the panel itself: the agent build and the panel of this release and of the one
// before it, which a revert starts again. A clean-up never removes them.
func platformImage(r imageRow) bool {
	return strings.HasPrefix(r.Repository, "nostek/") || strings.HasPrefix(r.Repository, "docker.io/nostek/")
}

// named is true when the image row is one of the kept references (name:tag@sha256:…, name@sha256:… or name:tag).
func named(r imageRow, keep map[string]bool) bool {
	for ref := range keep {
		name, digest, hasDigest := strings.Cut(ref, "@")
		repo, tag := name, ""
		if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
			repo, tag = name[:i], name[i+1:]
		}
		if repo != r.Repository && "docker.io/library/"+repo != r.Repository && "docker.io/"+repo != r.Repository {
			continue
		}
		if hasDigest && digest == r.Digest {
			return true
		}
		if !hasDigest && tag != "" && tag == r.Tag {
			return true
		}
	}
	return false
}

// CleanDocker removes what no website uses and returns what was freed, as Docker says it.
func (m *Manager) CleanDocker(ctx context.Context, what string) (string, error) {
	if what != CleanImages && what != CleanBuild {
		return "", fmt.Errorf("%w: clean-up %q is not offered", ErrBadInput, what)
	}
	if !m.mu.TryLock() { // never while a website is created, updated or built
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if what == CleanBuild {
		out, err := m.run.Docker(ctx, m.cfg.Data, nil, "builder", "prune", "--force", "--filter", "until=24h")
		if err != nil {
			return "", fmt.Errorf("clean-up failed: %s", tail(out))
		}
		for _, l := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Total:"); ok {
				return strings.TrimSpace(v), nil
			}
		}
		return "", nil
	}
	keep, err := m.keptImages()
	if err != nil {
		return "", err
	}
	raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "image", "ls", "--digests", "--no-trunc", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("docker image ls failed: %s", tail(raw))
	}
	before, _ := m.dockerDF(ctx)
	removed := 0
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r imageRow
		if json.Unmarshal([]byte(l), &r) != nil || r.ID == "" || platformImage(r) || named(r, keep) {
			continue
		}
		// without --force: Docker refuses an image a container still uses, which stays
		if _, err := m.run.Docker(ctx, m.cfg.Data, nil, "image", "rm", r.ID); err == nil {
			removed++
		}
	}
	after, _ := m.dockerDF(ctx)
	return freedText(before, after, removed), nil
}

func freedText(before, after []DockerUse, removed int) string {
	size := func(l []DockerUse) int64 {
		for _, d := range l {
			if d.Kind == "images" {
				return d.Bytes
			}
		}
		return 0
	}
	freed := size(before) - size(after)
	if freed < 0 {
		freed = 0
	}
	return fmt.Sprintf("%d images, %s", removed, humanBytes(freed))
}

// humanBytes writes a size the way Docker does (1000-based units).
func humanBytes(b int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	f, i := float64(b), 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", b)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
