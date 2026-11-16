package sites

// MIG-01: move an existing WordPress site into a WordPress website of the panel, from its old host through the
// Nostek Migrate plugin, or from a cPanel backup (or any zip/tar of a WordPress folder with a SQL dump). Both ways
// fill a staging folder and a SQL file; the import core then makes a before-migrate backup, swaps the files in
// (keeping the panel's own wp-config.php), loads the database, carries the table prefix and rewrites the addresses
// with WP-CLI. The old site is only read.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// MaxMigrate is the largest archive (or database file) a migration takes.
	MaxMigrate = 20 << 30
	// migrateEntries is the most files and folders a migrated site may hold.
	migrateEntries = 1_000_000
	// batchFiles and batchBytes size one files call to the plugin; larger files come in ranges of rangeBytes.
	batchFiles = 1000
	batchBytes = 64 << 20
	rangeBytes = 32 << 20
)

// MigrateRequest starts a migration. Way "plugin" needs Code; way "backup" uses the uploaded files or Link, and
// Root when the archive holds more than one WordPress folder.
type MigrateRequest struct {
	Way  string `json:"way"`
	Code string `json:"code,omitempty"`
	Link string `json:"link,omitempty"`
	Root string `json:"root,omitempty"`
}

// MigrateFiles tells the panel what was uploaded for a backup migration.
type MigrateFiles struct {
	Archive  int64    `json:"archive"`       // bytes, 0 = none
	Database int64    `json:"database"`      // bytes, 0 = none
	Job      *JobView `json:"job,omitempty"` // the running migration, else the newest one still kept
}

// skipDirs are left behind: caches and other backup plugins' archives (they are rebuilt or not wanted).
var skipDirs = []string{
	"wp-content/cache", "wp-content/upgrade", "wp-content/ai1wm-backups", "wp-content/updraft",
	"wp-content/backups-dup-lite", "wp-content/backups-dup-pro", "wp-content/backup-db", "wp-content/et-cache",
}

// dropIns point at the old host's cache and are moved aside after the move.
var dropIns = []string{"wp-content/advanced-cache.php", "wp-content/object-cache.php"}

// oldConfigGuard heads the old wp-config.php kept as wp-config.old.php, so it never runs.
const oldConfigGuard = "<?php exit; /* the old site's wp-config.php, kept by Nostek Panel for reference */ ?>\n"

var prefixRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

func (m *Manager) migrateDir(id string) string { return filepath.Join(m.dir(id), "migrate") }

func migratePart(part string) (string, error) {
	switch part {
	case "archive", "database":
		return part, nil
	}
	return "", fmt.Errorf("%w: upload part is archive or database", ErrBadInput)
}

func (m *Manager) migrateSite(id string) (Site, error) {
	s, err := m.readySite(id)
	if err != nil {
		return s, err
	}
	if s.AppName() != "wordpress" {
		return s, fmt.Errorf("%w: only a WordPress website can take a migrated WordPress site", ErrBadInput)
	}
	return s, nil
}

// MigrateUpload stores one uploaded file (the archive or the database) for a backup migration.
func (m *Manager) MigrateUpload(id, part string, body io.Reader) (int64, error) {
	if _, err := m.migrateSite(id); err != nil {
		return 0, err
	}
	part, err := migratePart(part)
	if err != nil {
		return 0, err
	}
	if m.migrating(id) {
		return 0, ErrBusy
	}
	dir := m.migrateDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	tmp := filepath.Join(dir, part+".part-"+randHex(4))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp)
	n, err := io.Copy(f, &diskGuard{r: io.LimitReader(body, MaxMigrate+1), dir: dir})
	if err = errors.Join(err, f.Close()); err != nil {
		return 0, err
	}
	if n > MaxMigrate {
		return 0, ErrTooBig
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: the file is empty", ErrBadInput)
	}
	return n, os.Rename(tmp, filepath.Join(dir, part))
}

// diskGuard stops an upload or download before it fills the disk (keeps 1 GB free).
type diskGuard struct {
	r    io.Reader
	dir  string
	seen int64
}

func (d *diskGuard) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.seen += int64(n)
	if d.seen%(256<<20) < int64(n) { // about every 256 MB
		if free := freeBytes(d.dir); free >= 0 && free < 1<<30 {
			return n, fmt.Errorf("%w: the server disk is almost full", ErrBadInput)
		}
	}
	return n, err
}

// migrating is true while a migration of the website runs.
func (m *Manager) migrating(id string) bool {
	for _, v := range m.jobs.List() {
		if v.Site == id && v.Kind == "migrate" && v.State == "running" {
			return true
		}
	}
	return false
}

// MigrateStatus lists the uploaded files.
func (m *Manager) MigrateStatus(id string) (MigrateFiles, error) {
	if _, err := m.migrateSite(id); err != nil {
		return MigrateFiles{}, err
	}
	var f MigrateFiles
	for _, v := range m.jobs.List() {
		if v.Site == id && v.Kind == "migrate" && (f.Job == nil || f.Job.State != "running" && (v.State == "running" || v.Started.After(f.Job.Started))) {
			v := v
			f.Job = &v
		}
	}
	if fi, err := os.Stat(filepath.Join(m.migrateDir(id), "archive")); err == nil {
		f.Archive = fi.Size()
	}
	if fi, err := os.Stat(filepath.Join(m.migrateDir(id), "database")); err == nil {
		f.Database = fi.Size()
	}
	return f, nil
}

// MigrateClear removes the uploaded files.
func (m *Manager) MigrateClear(id string) error {
	if _, err := m.migrateSite(id); err != nil {
		return err
	}
	if m.migrating(id) {
		return ErrBusy
	}
	return os.RemoveAll(m.migrateDir(id))
}

// StartMigrate checks the request and runs the migration as a job.
func (m *Manager) StartMigrate(id string, req MigrateRequest) (string, error) {
	s, err := m.migrateSite(id)
	if err != nil {
		return "", err
	}
	var code MigrateCode
	var link *url.URL
	switch req.Way {
	case "plugin":
		if code, err = ParseMigrateCode(req.Code); err != nil {
			return "", err
		}
	case "backup":
		if req.Link != "" {
			if link, err = url.Parse(strings.TrimSpace(req.Link)); err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil {
				return "", fmt.Errorf("%w: the download link must be an https address", ErrBadInput)
			}
		} else if _, err := os.Stat(filepath.Join(m.migrateDir(id), "archive")); err != nil {
			return "", fmt.Errorf("%w: upload the backup first, or give its download link", ErrBadInput)
		}
		if req.Root != "" {
			if _, ok := safeRel(req.Root); !ok && req.Root != "." {
				return "", fmt.Errorf("%w: unknown WordPress folder", ErrBadInput)
			}
		}
	default:
		return "", fmt.Errorf("%w: migrate by plugin or backup", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("migrate", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		mg := &migration{m: m, j: j, s: s}
		defer mg.cleanup()
		var err error
		if req.Way == "plugin" {
			err = mg.fromPlugin(ctx, code)
		} else {
			err = mg.fromBackup(ctx, link, req.Root)
		}
		if err != nil {
			if mg.safe != "" {
				err = fmt.Errorf("%w — the website can be put back with the backup %s", err, mg.safe)
			}
			j.Fail(err.Error())
			return
		}
		if req.Way == "backup" {
			_ = os.RemoveAll(m.migrateDir(id))
		}
		j.Step("done " + mg.summary())
		j.Done("")
	}()
	return j.ID, nil
}

// migration is one running migration: where its files and database are staged, and what it found.
type migration struct {
	m     *Manager
	j     *Job
	s     Site
	stage string   // staging folder for the files (inside the website folder, same disk)
	root  *os.Root // open on stage
	dump  string   // SQL file
	safe  string   // the before-migrate backup
	// found on the old site
	prefix   string
	oldHome  string
	version  string
	files    int64
	bytes    int64
	tables   int
	admins   int
	movedOut []string
}

func (mg *migration) cleanup() {
	if mg.root != nil {
		mg.root.Close()
	}
	if mg.stage != "" {
		_ = os.RemoveAll(mg.stage)
	}
	if mg.dump != "" {
		_ = os.Remove(mg.dump)
	}
}

func (mg *migration) summary() string {
	s := fmt.Sprintf("%s → %s, WordPress %s, %d files, %d tables, %d administrators, backup %s", mg.oldHome, mg.s.URL, mg.version, mg.files, mg.tables, mg.admins, mg.safe)
	if len(mg.movedOut) > 0 {
		s += ", moved aside: " + strings.Join(mg.movedOut, " ")
	}
	return s
}

// open makes the staging folder and the SQL file.
func (mg *migration) open() error {
	dir := mg.m.migrateDir(mg.s.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	mg.stage = filepath.Join(mg.m.dir(mg.s.ID), "wp.migrate-"+randHex(4))
	if err := os.Mkdir(mg.stage, 0o755); err != nil {
		return err
	}
	var err error
	if mg.root, err = os.OpenRoot(mg.stage); err != nil {
		return err
	}
	mg.dump = filepath.Join(dir, "dump-"+randHex(4)+".sql")
	return nil
}

// skipped is true for paths left behind (caches, other backup plugins' archives, the old wp-config.php is kept
// under another name by put).
func skipped(rel string) bool {
	for _, d := range skipDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// safeRel cleans a path from the old site or an archive; ok is false for anything leaving the WordPress folder.
func safeRel(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return "", false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == ".." || part == "" {
			return "", false
		}
	}
	return path.Clean(name), true
}

// put writes one entry into the staging folder as the website's files: owner www-data (Docker; the native switch
// gives them to the website user), rwx bits only, links as links. The old wp-config.php is kept as
// wp-config.old.php: the website runs on the panel's own.
func (mg *migration) put(rel string, typ byte, mode fs.FileMode, mtime time.Time, link string, size int64, r io.Reader) error {
	if rel == "wp-config.php" && (typ == tar.TypeReg || typ == tar.TypeRegA) {
		// kept for reference only: it stops at once if anything opens it over the web
		if size > 1<<20 {
			return nil
		}
		b, err := io.ReadAll(io.LimitReader(r, size))
		if err != nil {
			return err
		}
		b = append([]byte(oldConfigGuard), b...)
		rel, r, size = "wp-config.old.php", bytes.NewReader(b), int64(len(b))
		mg.files--
		mg.bytes -= size
	}
	if skipped(rel) {
		return nil
	}
	root := mg.root
	if d := path.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	uid := mg.m.uid()
	mode &= 0o777
	switch typ {
	case tar.TypeDir:
		if err := root.MkdirAll(rel, 0o755); err != nil {
			return err
		}
		mode |= 0o700
	case tar.TypeSymlink:
		_ = root.Remove(rel)
		if err := root.Symlink(link, rel); err != nil {
			return err
		}
		return root.Lchown(rel, uid, uid)
	case tar.TypeReg, tar.TypeRegA:
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(r, size+1))
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		if n != size {
			return fmt.Errorf("%s: %d of %d bytes arrived", rel, n, size)
		}
		mode |= 0o600
		mg.files++
		mg.bytes += size
	default:
		return nil // devices, fifos and hard links are not website files
	}
	if err := root.Chown(rel, uid, uid); err != nil {
		return err
	}
	if err := root.Chmod(rel, mode); err != nil {
		return err
	}
	return root.Chtimes(rel, mtime, mtime)
}

// ---------- way 1: the plugin ----------

func (mg *migration) fromPlugin(ctx context.Context, code MigrateCode) error {
	mg.j.Step("inspect")
	p, info, err := mg.m.connectPlugin(ctx, code)
	if err != nil {
		return err
	}
	if info.Multisite {
		return fmt.Errorf("%w: the old site is a WordPress multisite, which cannot be moved yet", ErrBadInput)
	}
	if !prefixRe.MatchString(info.Prefix) {
		return fmt.Errorf("%w: the old site's table prefix %q cannot be used", ErrBadInput, info.Prefix)
	}
	mg.prefix, mg.version = info.Prefix, info.WordPress
	if err := mg.open(); err != nil {
		return err
	}
	if err := mg.safety(ctx); err != nil {
		return err
	}

	// files: list everything, then fetch in batches (large files in ranges)
	mg.j.Step("copy files")
	var small []PluginEntry
	var large []PluginEntry
	var total int64
	if err := p.list(ctx, migrateEntries, func(e PluginEntry) error {
		rel, ok := safeRel(e.Path)
		if !ok {
			return fmt.Errorf("%w: the old site listed a path outside its folder: %q", ErrBadInput, e.Path)
		}
		if skipped(rel) {
			return nil
		}
		e.Path = rel
		mt := time.Unix(e.MTime, 0)
		switch e.Type {
		case "dir":
			return mg.put(rel, tar.TypeDir, fs.FileMode(e.Mode), mt, "", 0, nil)
		case "link":
			return mg.put(rel, tar.TypeSymlink, 0o777, mt, e.Target, 0, nil)
		case "file":
			total += e.Size
			if e.Size > batchBytes {
				large = append(large, e)
			} else {
				small = append(small, e)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if free := freeBytes(mg.m.cfg.Data); free >= 0 && free < total+(1<<30) {
		return fmt.Errorf("%w: not enough free disk for the old site's files (%d MB free, %d MB needed)", ErrBadInput, free>>20, (total+(1<<30))>>20)
	}
	want := map[string]PluginEntry{}
	for _, e := range small {
		want[e.Path] = e
	}
	for len(small) > 0 {
		n, sz := 0, int64(0)
		for n < len(small) && n < batchFiles && (n == 0 || sz+small[n].Size <= batchBytes) {
			sz += small[n].Size
			n++
		}
		paths := make([]string, n)
		for i := range paths {
			paths[i] = small[i].Path
		}
		got := map[string]bool{}
		if err := p.batch(ctx, paths, func(h *tar.Header, r io.Reader) error {
			rel, ok := safeRel(h.Name)
			e, asked := want[rel]
			if !ok || !asked || got[rel] {
				return fmt.Errorf("%w: the old site sent a file that was not asked for: %q", ErrBadInput, h.Name)
			}
			got[rel] = true
			if h.Typeflag == tar.TypeSymlink {
				return mg.put(rel, tar.TypeSymlink, 0o777, h.ModTime, h.Linkname, 0, nil)
			}
			return mg.put(rel, tar.TypeReg, fs.FileMode(e.Mode), time.Unix(e.MTime, 0), "", h.Size, r)
		}); err != nil {
			return err
		}
		// the plugin stops a batch at its size limit; files it skipped (gone since the listing) are left out
		done := 0
		for done < n && got[small[done].Path] {
			done++
		}
		if done == 0 {
			if _, err := mg.root.Lstat(small[0].Path); err != nil {
				done = 1 // the file disappeared on the old site
			}
		}
		for i := done; i < n; i++ {
			if got[small[i].Path] {
				done = i + 1
			}
		}
		small = small[done:]
		mg.j.Progress(fmt.Sprintf("%d/%d MB", mg.bytes>>20, total>>20))
	}
	for _, e := range large {
		if err := mg.fetchLarge(ctx, p, e); err != nil {
			return err
		}
		mg.j.Progress(fmt.Sprintf("%d/%d MB", mg.bytes>>20, total>>20))
	}

	// database: every table with the site's prefix, piece by piece
	mg.j.Step("copy database")
	f, err := os.OpenFile(mg.dump, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprintf(w, "SET NAMES utf8mb4;\nSET FOREIGN_KEY_CHECKS=0;\nSET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n")
	var rows int64
	for _, t := range info.Tables {
		if !strings.HasPrefix(t.Name, info.Prefix) {
			continue // another application sharing the old database
		}
		n, err := p.tableTo(ctx, t.Name, w)
		if err != nil {
			f.Close()
			return err
		}
		rows += n
		mg.tables++
		mg.j.Progress(fmt.Sprintf("%d/%d tables", mg.tables, len(info.Tables)))
	}
	fmt.Fprintf(w, "SET FOREIGN_KEY_CHECKS=1;\n")
	if err := errors.Join(w.Flush(), f.Close()); err != nil {
		return err
	}
	if mg.tables == 0 {
		return fmt.Errorf("%w: the old site has no tables with its prefix %s", ErrBadInput, info.Prefix)
	}
	if err := mg.install(ctx); err != nil {
		return err
	}
	if err := p.finish(ctx); err != nil {
		mg.movedOut = append(mg.movedOut, "(the old site's code is still valid: make a new one or remove the plugin there)")
	}
	return nil
}

// fetchLarge copies one large file range by range.
func (mg *migration) fetchLarge(ctx context.Context, p *plugin, e PluginEntry) error {
	tmp := filepath.Join(mg.m.migrateDir(mg.s.ID), "large-"+randHex(4))
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	defer f.Close()
	for off := int64(0); off < e.Size; {
		n, err := p.rangeTo(ctx, e.Path, off, min(rangeBytes, e.Size-off), f)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%s: the old site sent nothing at %d of %d bytes", e.Path, off, e.Size)
		}
		off += n
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return mg.put(e.Path, tar.TypeReg, fs.FileMode(e.Mode), time.Unix(e.MTime, 0), "", e.Size, f)
}

// ---------- way 2: a cPanel (or plain) backup ----------

// arcEntry is one archive entry, whatever the format.
type arcEntry struct {
	name  string
	typ   byte
	mode  fs.FileMode
	mtime time.Time
	link  string
	size  int64
}

// walkArchive reads a zip, tar or tar.gz; a nested homedir.tar (cPanel) is read as part of the stream, its entries
// named under <folder>/homedir/.
func walkArchive(p string, fn func(e arcEntry, r io.Reader) error) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if n == 4 && bytes.Equal(head, []byte("PK\x03\x04")) {
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(f, fi.Size())
		if err != nil {
			return fmt.Errorf("%w: the zip file is damaged: %v", ErrBadInput, err)
		}
		for _, z := range zr.File {
			e := arcEntry{name: z.Name, mode: z.Mode().Perm(), mtime: z.Modified, size: int64(z.UncompressedSize64), typ: tar.TypeReg}
			switch {
			case z.Mode()&fs.ModeSymlink != 0:
				e.typ = tar.TypeSymlink
			case z.Mode().IsDir() || strings.HasSuffix(z.Name, "/"):
				e.typ = tar.TypeDir
			case !z.Mode().IsRegular():
				continue
			}
			rc, err := z.Open()
			if err != nil {
				return fmt.Errorf("%w: %s in the zip: %v", ErrBadInput, z.Name, err)
			}
			if e.typ == tar.TypeSymlink {
				b, _ := io.ReadAll(io.LimitReader(rc, 4096))
				e.link = string(b)
			}
			err = fn(e, rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	var r io.Reader = bufio.NewReaderSize(f, 1<<20)
	if n >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("%w: not a valid .tar.gz file", ErrBadInput)
		}
		defer gz.Close()
		r = gz
	}
	return walkTar(tar.NewReader(r), "", fn, 0)
}

func walkTar(tr *tar.Reader, under string, fn func(e arcEntry, r io.Reader) error, depth int) error {
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if depth == 0 && under == "" {
				return fmt.Errorf("%w: the backup is not a readable zip, tar or tar.gz file (%v)", ErrBadInput, err)
			}
			return fmt.Errorf("%w: the backup is damaged: %v", ErrBadInput, err)
		}
		name := under + strings.TrimPrefix(h.Name, "./")
		if depth == 0 && (path.Base(name) == "homedir.tar") && h.Typeflag == tar.TypeReg {
			if err := walkTar(tar.NewReader(tr), strings.TrimSuffix(name, "homedir.tar")+"homedir/", fn, depth+1); err != nil {
				return err
			}
			continue
		}
		e := arcEntry{name: name, typ: h.Typeflag, mode: fs.FileMode(h.Mode).Perm(), mtime: h.ModTime, link: h.Linkname, size: h.Size}
		if err := fn(e, tr); err != nil {
			return err
		}
	}
}

var (
	dbNameRe    = regexp.MustCompile(`define\s*\(\s*['"]DB_NAME['"]\s*,\s*['"]([^'"]{1,64})['"]`)
	tablePrefRe = regexp.MustCompile(`\$table_prefix\s*=\s*['"]([^'"]{0,64})['"]\s*;`)
	multisiteRe = regexp.MustCompile(`define\s*\(\s*['"]MULTISITE['"]\s*,\s*(true|1)\s*\)`)
	versionRe   = regexp.MustCompile(`\$wp_version\s*=\s*['"]([0-9A-Za-z.\-]{1,32})['"]`)
)

// candidate is a WordPress folder found in an archive.
type candidate struct {
	dir     string // folder name inside the archive ("" = the top)
	config  []byte // its wp-config.php (or the one in the folder above, as WordPress allows)
	version string
}

// inspect reads the archive once: the WordPress folders, their wp-config.php and the SQL dumps it holds.
func inspectArchive(p string) (map[string]*candidate, []string, error) {
	configs := map[string][]byte{}
	versions := map[string]string{}
	var dumps []string
	n := 0
	err := walkArchive(p, func(e arcEntry, r io.Reader) error {
		if n++; n > migrateEntries+10 {
			return fmt.Errorf("%w: the backup has more than %d files and folders", ErrBadInput, migrateEntries)
		}
		name, ok := safeRel(e.name)
		if !ok {
			if strings.Trim(e.name, "./") == "" {
				return nil
			}
			return fmt.Errorf("%w: the backup holds a path outside it: %q", ErrBadInput, e.name)
		}
		if e.typ != tar.TypeReg && e.typ != tar.TypeRegA {
			return nil
		}
		base := path.Base(name)
		switch {
		case base == "wp-config.php" && e.size < 256<<10:
			b, _ := io.ReadAll(io.LimitReader(r, 256<<10))
			configs[path.Dir(name)] = b
		case strings.HasSuffix(name, "/wp-includes/version.php") || name == "wp-includes/version.php":
			b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
			v := ""
			if mm := versionRe.FindSubmatch(b); mm != nil {
				v = string(mm[1])
			}
			versions[path.Dir(path.Dir(name))] = v
		case strings.HasSuffix(base, ".sql") || strings.HasSuffix(base, ".sql.gz"):
			dumps = append(dumps, name)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	found := map[string]*candidate{}
	for dir, v := range versions {
		c := &candidate{dir: dir, version: v, config: configs[dir]}
		if c.config == nil && dir != "." {
			c.config = configs[path.Dir(dir)]
		}
		if c.config == nil {
			continue // a copy of WordPress without a configuration is not a site
		}
		if skippedIn(dir) {
			continue
		}
		found[dir] = c
	}
	sort.Strings(dumps)
	return found, dumps, nil
}

// skippedIn is true for WordPress copies inside another site's backup or cache folders.
func skippedIn(dir string) bool {
	for _, d := range skipDirs {
		if strings.Contains("/"+dir+"/", "/"+d+"/") {
			return true
		}
	}
	return false
}

// pickDump chooses the database: the uploaded file, else the dump named after DB_NAME, else the only one.
func pickDump(dumps []string, cfg []byte) (string, error) {
	if len(dumps) == 1 {
		return dumps[0], nil
	}
	if mm := dbNameRe.FindSubmatch(cfg); mm != nil {
		db := string(mm[1])
		for _, d := range dumps {
			b := path.Base(d)
			if b == db+".sql" || b == db+".sql.gz" {
				return d, nil
			}
		}
	}
	if len(dumps) == 0 {
		return "", fmt.Errorf("%w: the backup holds no database (.sql): upload the database backup as a second file", ErrBadInput)
	}
	return "", fmt.Errorf("%w: the backup holds several databases and none matches the site's DB_NAME: %s", ErrBadInput, strings.Join(dumps, ", "))
}

func (mg *migration) fromBackup(ctx context.Context, link *url.URL, want string) error {
	dir := mg.m.migrateDir(mg.s.ID)
	arc := filepath.Join(dir, "archive")
	if link != nil {
		mg.j.Step("download")
		if err := mg.download(ctx, link, arc); err != nil {
			return err
		}
	}
	mg.j.Step("inspect")
	found, dumps, err := inspectArchive(arc)
	if err != nil {
		return err
	}
	var c *candidate
	switch {
	case want != "":
		if c = found[path.Clean(want)]; c == nil {
			return fmt.Errorf("%w: the backup has no WordPress folder %q", ErrBadInput, want)
		}
	case len(found) == 1:
		for _, v := range found {
			c = v
		}
	case len(found) == 0:
		return fmt.Errorf("%w: no WordPress site (wp-includes and wp-config.php) was found in the backup", ErrBadInput)
	default:
		names := make([]string, 0, len(found))
		for k := range found {
			names = append(names, k)
		}
		sort.Strings(names)
		return fmt.Errorf("%w: the backup holds several WordPress sites; choose one: %s", ErrChoose, strings.Join(names, ", "))
	}
	if multisiteRe.Match(c.config) {
		return fmt.Errorf("%w: the old site is a WordPress multisite, which cannot be moved yet", ErrBadInput)
	}
	mg.prefix = "wp_"
	if mm := tablePrefRe.FindSubmatch(c.config); mm != nil {
		mg.prefix = string(mm[1])
	}
	if !prefixRe.MatchString(mg.prefix) {
		return fmt.Errorf("%w: the old site's table prefix %q cannot be used", ErrBadInput, mg.prefix)
	}
	mg.version = c.version
	dbFile := filepath.Join(dir, "database")
	dumpIn := ""
	if _, err := os.Stat(dbFile); err != nil {
		if dumpIn, err = pickDump(dumps, c.config); err != nil {
			return err
		}
	}
	fi, err := os.Stat(arc)
	if err != nil {
		return err
	}
	if free := freeBytes(mg.m.cfg.Data); free >= 0 && free < fi.Size()*3/2+(1<<30) {
		return fmt.Errorf("%w: not enough free disk to unpack the backup (%d MB free)", ErrBadInput, free>>20)
	}
	if err := mg.open(); err != nil {
		return err
	}
	if err := mg.safety(ctx); err != nil {
		return err
	}
	mg.j.Step("copy files")
	prefix := ""
	if c.dir != "." {
		prefix = c.dir + "/"
	}
	err = walkArchive(arc, func(e arcEntry, r io.Reader) error {
		name, ok := safeRel(e.name)
		if !ok {
			return nil // checked by inspect
		}
		if dumpIn != "" && name == dumpIn {
			return copyDump(r, name, mg.dump)
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			return nil
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || rel == "." {
			return nil
		}
		if err := mg.put(rel, e.typ, e.mode, e.mtime, e.link, e.size, r); err != nil {
			return fmt.Errorf("unpacking %s: %w", rel, err)
		}
		if mg.files%2000 == 0 && e.typ == tar.TypeReg {
			mg.j.Progress(fmt.Sprintf("%d files", mg.files))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := mg.root.Lstat("wp-includes/version.php"); err != nil {
		return fmt.Errorf("%w: the WordPress files did not unpack", ErrBadInput)
	}
	if dumpIn == "" {
		f, err := os.Open(dbFile)
		if err != nil {
			return err
		}
		err = copyDump(f, "database", mg.dump)
		f.Close()
		if err != nil {
			return err
		}
	}
	mg.j.Step("copy database")
	return mg.install(ctx)
}

// ErrChoose asks the panel to pick one of the listed WordPress folders.
var ErrChoose = errors.New("choose")

// copyDump writes a dump (plain or gzip) to dst as plain SQL.
func copyDump(r io.Reader, name string, dst string) error {
	br := bufio.NewReaderSize(r, 1<<20)
	var in io.Reader = br
	if head, _ := br.Peek(2); len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("%w: %s is not a valid .gz file", ErrBadInput, name)
		}
		defer gz.Close()
		in = gz
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &diskGuard{r: in, dir: filepath.Dir(dst)})
	return errors.Join(err, f.Close())
}

// download fetches a backup over https through the public-address guard.
func (mg *migration) download(ctx context.Context, link *url.URL, dst string) error {
	c, err := mg.m.migrateClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "NostekPanel-Migrate/1")
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddr) {
			return fmt.Errorf("%w: %s does not resolve to a public internet address", ErrBadInput, link.Hostname())
		}
		return fmt.Errorf("the download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the download link answered %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxMigrate {
		return ErrTooBig
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	n, err := io.Copy(f, &diskGuard{r: io.LimitReader(resp.Body, MaxMigrate+1), dir: filepath.Dir(dst)})
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("the download broke off: %w", err)
	}
	if n > MaxMigrate {
		return ErrTooBig
	}
	mg.j.Progress(fmt.Sprintf("%d MB", n>>20))
	return os.Rename(tmp, dst)
}

// ---------- the import core ----------

// safety makes the before-migrate backup of the fresh website.
func (mg *migration) safety(ctx context.Context) error {
	name, err := mg.m.backup(ctx, mg.j, mg.s, "before-migrate")
	if err != nil {
		return fmt.Errorf("the safety backup before the move failed, nothing was changed: %w", err)
	}
	mg.safe = name
	return nil
}

var (
	createDBRe = regexp.MustCompile(`(?i)^\s*(CREATE\s+DATABASE|USE\s+)`)
	definerRe  = regexp.MustCompile("(?i)DEFINER\\s*=\\s*(`[^`]*`|'[^']*'|[^\\s@]+)@(`[^`]*`|'[^']*'|[^\\s*]+)")
)

// sqlFilter drops CREATE DATABASE and USE lines (the website's own database is used) and DEFINER clauses (the old
// host's users do not exist here) while the dump streams.
type sqlFilter struct {
	br  *bufio.Reader
	buf []byte
	err error
}

func (f *sqlFilter) Read(p []byte) (int, error) {
	for len(f.buf) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		line, err := f.br.ReadBytes('\n')
		f.err = err
		if createDBRe.Match(line) {
			continue
		}
		if bytes.Contains(line, []byte("DEFINER")) || bytes.Contains(line, []byte("definer")) {
			line = definerRe.ReplaceAll(line, nil)
		}
		f.buf = line
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

// install swaps the files in, loads the database, carries the prefix, rewrites the addresses and checks the site.
func (mg *migration) install(ctx context.Context) error {
	m, s := mg.m, mg.s
	// the panel's own wp-config.php stays: it holds the new database login and the HTTPS lines
	cfg, err := os.ReadFile(filepath.Join(m.wpDir(s.ID), "wp-config.php"))
	if err != nil {
		return fmt.Errorf("the website's own wp-config.php is missing: %w", err)
	}
	uid := m.uid()
	if err := writeRootFile(mg.root, "wp-config.php", cfg, 0o640, uid); err != nil {
		return err
	}
	mg.root.Close()
	mg.root = nil

	// carry the table prefix: Docker reads it from compose (WORDPRESS_TABLE_PREFIX), native from wp-config.php
	next, err := m.setPrefix(s, mg.prefix)
	if err != nil {
		return err
	}
	mg.s, s = next, next

	mg.j.Step("load database")
	f, err := os.Open(mg.dump)
	if err != nil {
		return err
	}
	in := &sqlFilter{br: bufio.NewReaderSize(f, 1<<20)}
	var out []byte
	if s.IsNative() {
		out, err = m.nativeLoad(ctx, s, in)
		if err != nil {
			err = dbErr(out)
		}
	} else {
		reset, rargs := m.resetArgs(s)
		if out, err = m.compose(ctx, s.ID, strings.NewReader(reset), rargs...); err == nil {
			out, err = m.compose(ctx, s.ID, in, m.loadArgs(s)...)
		}
		if err != nil {
			err = sqlErr(s, out)
		}
	}
	f.Close()
	if err != nil {
		return fmt.Errorf("loading the database failed: %w", err)
	}

	mg.j.Step("switch files")
	stage := mg.stage
	if err := m.switchFiles(ctx, s, stage, func() { mg.stage = "" }); err != nil {
		return err
	}
	if s.IsNative() {
		if err := m.applyPrefix(ctx, s); err != nil {
			return err
		}
	}

	mg.j.Step("addresses")
	r, err := m.wp(ctx, s.ID, nil, []string{"wp", "option", "get", "home"})
	if err != nil || r[0].RC != 0 {
		return fmt.Errorf("WordPress does not load with the old site's database: %s", wpText(r, err))
	}
	mg.oldHome = strings.TrimSpace(r[0].Out)
	cmds, err := replaceCmds(mg.oldHome, s.URL)
	if err != nil {
		return err
	}
	res, err := m.wp(ctx, s.ID, nil, cmds...)
	if err != nil {
		return fmt.Errorf("rewriting the addresses failed: %w", err)
	}
	for i, x := range res {
		if x.RC != 0 {
			return fmt.Errorf("rewriting the addresses failed (%s): %s", strings.Join(cmds[i][1:3], " "), tail([]byte(x.Err+x.Out)))
		}
	}

	mg.j.Step("tidy")
	for _, d := range dropIns {
		p := filepath.Join(m.wpDir(s.ID), d)
		if _, err := os.Lstat(p); err == nil {
			if os.Rename(p, p+".nostek-off") == nil {
				mg.movedOut = append(mg.movedOut, d)
			}
		}
	}
	// the copied plugin and its key must not work on the new site
	_, _ = m.wp(ctx, s.ID, nil,
		[]string{"wp", "option", "delete", "nostek_migrate_key"},
		[]string{"wp", "plugin", "deactivate", "nostek-migrate", "--uninstall"},
		[]string{"wp", "plugin", "delete", "nostek-migrate"},
		[]string{"wp", "cache", "flush"},
		[]string{"wp", "rewrite", "flush"})

	mg.j.Step("check")
	res, err = m.wp(ctx, s.ID, nil, []string{"wp", "option", "get", "home"}, []string{"wp", "user", "list", "--role=administrator", "--field=ID"})
	if err != nil {
		return fmt.Errorf("the check failed: %w", err)
	}
	if got := strings.TrimSpace(res[0].Out); res[0].RC != 0 || strings.TrimRight(got, "/") != strings.TrimRight(s.URL, "/") {
		return fmt.Errorf("the check failed: the site's address is %q, not %s", got, s.URL)
	}
	for _, l := range strings.Split(res[1].Out, "\n") {
		if strings.TrimSpace(l) != "" {
			mg.admins++
		}
	}
	if res[1].RC != 0 || mg.admins == 0 {
		return errors.New("the check failed: the moved site has no administrator")
	}
	return nil
}

// setPrefix records a WordPress website's table prefix ("wp_" is stored as ""): in site.json, and for Docker in
// compose.yml (WORDPRESS_TABLE_PREFIX), which takes effect when the containers are next recreated.
func (m *Manager) setPrefix(s Site, prefix string) (Site, error) {
	if prefix == "wp_" {
		prefix = ""
	}
	if prefix != "" && !prefixRe.MatchString(prefix) {
		return s, fmt.Errorf("%w: table prefix %q cannot be used", ErrBadInput, prefix)
	}
	next := s
	next.TablePrefix = prefix
	if !s.IsNative() {
		cf, err := next.Compose()
		if err != nil {
			return s, err
		}
		prev, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
		cf = keepImages(cf, string(prev))
		if err := writeFile(filepath.Join(m.dir(s.ID), "compose.yml"), []byte(cf), 0o644); err != nil {
			return s, err
		}
	}
	return next, writeSite(m.dir(s.ID), next)
}

// replaceCmds rewrites the old address to the new one everywhere (WP-CLI handles serialized data): the
// scheme-less form covers http, https and //host links, then its JSON-escaped form, then the new address is made
// https, and home/siteurl are set.
func replaceCmds(oldHome, newURL string) ([][]string, error) {
	o, err := url.Parse(oldHome)
	if err != nil || o.Host == "" {
		return nil, fmt.Errorf("%w: the old site's address %q cannot be read", ErrBadInput, oldHome)
	}
	n, err := url.Parse(newURL)
	if err != nil || n.Host == "" {
		return nil, fmt.Errorf("the website's address %q cannot be read", newURL)
	}
	from := "//" + o.Host + strings.TrimRight(o.Path, "/")
	to := "//" + n.Host + strings.TrimRight(n.Path, "/")
	sr := func(a, b string) []string {
		return []string{"wp", "search-replace", a, b, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--report-changed-only"}
	}
	cmds := [][]string{}
	if from != to {
		cmds = append(cmds, sr(from, to), sr(strings.ReplaceAll(from, "/", `\/`), strings.ReplaceAll(to, "/", `\/`)))
	}
	cmds = append(cmds, sr("http:"+to, "https:"+to), sr(`http:`+strings.ReplaceAll(to, "/", `\/`), `https:`+strings.ReplaceAll(to, "/", `\/`)))
	home := "https:" + to
	cmds = append(cmds, []string{"wp", "option", "update", "home", home}, []string{"wp", "option", "update", "siteurl", home})
	return cmds, nil
}

func wpText(r []wpOut, err error) string {
	if err != nil {
		return err.Error()
	}
	if len(r) == 0 {
		return "no answer"
	}
	return tail([]byte(r[0].Err + r[0].Out))
}

// writeRootFile writes a small file inside root.
func writeRootFile(root *os.Root, name string, b []byte, mode fs.FileMode, uid int) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := root.Chown(name, uid, uid); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}
