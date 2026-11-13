package sites

// Website backups (BAK-01). One backup is one .tar.gz in the site's own backups folder:
//
//	manifest.json   what is inside (format, site, domain, server, PHP, sizes, time)
//	site.json       the site's settings at backup time
//	database.sql    a consistent mariadb-dump (single transaction)
//	wp/...          every file of the website with its owner, permissions and links (links are stored, never followed)
//
// The folder sits under /srv/nostek, so the nightly restic job (HOST-02) also carries every backup off the server.
// A restore always takes a "before-restore" backup first; if that copy fails, nothing is changed.

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// BackupFormat names the archive layout; a restore refuses any other.
	BackupFormat = "nostek-site-backup/1"
	// KeepDefault is how many daily backups are kept when the owner has not chosen.
	KeepDefault = 7
	// KeepMax is the most daily backups the owner may keep.
	KeepMax = 30
	// keepSafety is how many before-restore (and before-update) copies are kept.
	keepSafety = 3
	// DailyHour is the WIB hour of the daily backup (before the nightly off-site copy at 03:30).
	DailyHour = 2
	// BackupEvery is how often the agent looks for due daily backups.
	BackupEvery = 10 * time.Minute
)

var backupRe = regexp.MustCompile(`^(manual|daily|before-restore|before-update)-[0-9]{8}-[0-9]{6}\.tar\.gz$`)

// Backup is one archive of a website.
type Backup struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // manual | daily | before-restore | before-update
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// Backups is the backup page of a website.
type Backups struct {
	Backups []Backup `json:"backups"`
	Daily   bool     `json:"daily"`
	Keep    int      `json:"keep"`
	Free    int64    `json:"free"` // bytes free on the backup disk
	Job     *JobView `json:"job,omitempty"`
}

// Manifest is manifest.json inside a backup.
type Manifest struct {
	Format   string `json:"format"`
	Site     string `json:"site"`
	Domain   string `json:"domain"`
	Server   string `json:"server"`
	PHP      string `json:"php"`
	Kind     string `json:"kind"`
	Created  int64  `json:"created"`
	Files    int64  `json:"files"`
	Bytes    int64  `json:"bytes"`
	DBBytes  int64  `json:"db_bytes"`
	Agent    string `json:"agent,omitempty"`
	Complete bool   `json:"complete"`
}

func (m *Manager) backupDir(id string) string { return filepath.Join(m.dir(id), "backups") }

// backupName checks a backup file name from a request.
func backupName(name string) error {
	if !backupRe.MatchString(name) {
		return fmt.Errorf("%w: not a backup name", ErrBadInput)
	}
	return nil
}

func freeBytes(p string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// running returns the running backup or restore job of a site, if any.
func (m *Manager) running(id string) *JobView {
	for _, v := range m.jobs.List() {
		if v.Site == id && v.State == "running" && (v.Kind == "backup" || v.Kind == "restore") {
			v := v
			return &v
		}
	}
	return nil
}

// ListBackups returns a site's backups, newest first, with its schedule.
func (m *Manager) ListBackups(id string) (Backups, error) {
	s, err := m.Get(id)
	if err != nil {
		return Backups{}, err
	}
	b := Backups{Backups: []Backup{}, Daily: s.BackupDaily, Keep: s.keep(), Free: freeBytes(m.cfg.Data), Job: m.running(id)}
	ents, _ := os.ReadDir(m.backupDir(id))
	for _, e := range ents {
		if !backupRe.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		b.Backups = append(b.Backups, Backup{Name: e.Name(), Kind: strings.SplitN(e.Name(), "-2", 2)[0], Size: fi.Size(), Created: fi.ModTime().Unix()})
	}
	sort.Slice(b.Backups, func(i, j int) bool { return b.Backups[i].Created > b.Backups[j].Created })
	return b, nil
}

func (s Site) keep() int {
	if s.BackupKeep < 1 {
		return KeepDefault
	}
	return s.BackupKeep
}

// SetSchedule turns the daily backup on or off and sets how many daily backups are kept.
func (m *Manager) SetSchedule(id string, daily bool, keep int) error {
	if keep < 1 || keep > KeepMax {
		return fmt.Errorf("%w: keep must be 1..%d", ErrBadInput, KeepMax)
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	s.BackupDaily, s.BackupKeep = daily, keep
	if err := writeSite(m.dir(id), s); err != nil {
		return err
	}
	m.prune(id, "daily", keep)
	return nil
}

// readySite returns a site that finished creating.
func (m *Manager) readySite(id string) (Site, error) {
	s, err := m.Get(id)
	if err != nil {
		return s, err
	}
	if s.Phase != "ready" {
		return s, fmt.Errorf("%w: the website is not ready", ErrBadInput)
	}
	return s, nil
}

// StartBackup starts a backup job ("manual" from the panel).
func (m *Manager) StartBackup(id string) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("backup", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		name, err := m.backup(ctx, j, s, "manual")
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Step("done " + name)
		j.Done("")
	}()
	return j.ID, nil
}

// OpenBackup opens a backup for download.
func (m *Manager) OpenBackup(id, name string) (*os.File, fs.FileInfo, error) {
	if _, err := m.Get(id); err != nil {
		return nil, nil, err
	}
	if err := backupName(name); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(filepath.Join(m.backupDir(id), name))
	if err != nil {
		return nil, nil, ErrNotFound
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, ErrNotFound
	}
	return f, fi, nil
}

// DeleteBackup removes one backup.
func (m *Manager) DeleteBackup(id, name string) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if err := backupName(name); err != nil {
		return err
	}
	if m.running(id) != nil {
		return ErrBusy
	}
	if err := os.Remove(filepath.Join(m.backupDir(id), name)); err != nil {
		return ErrNotFound
	}
	return nil
}

// prune keeps the newest n backups of one kind.
func (m *Manager) prune(id, kind string, n int) {
	b, err := m.ListBackups(id)
	if err != nil {
		return
	}
	kept := 0
	for _, x := range b.Backups {
		if x.Kind != kind {
			continue
		}
		kept++
		if kept > n {
			_ = os.Remove(filepath.Join(m.backupDir(id), x.Name))
		}
	}
}

// siteBytes adds up the website files (for the free-space check).
func (m *Manager) siteBytes(id string) int64 {
	var n int64
	_ = filepath.WalkDir(m.wpDir(id), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// backup writes one archive; the caller holds m.mu. It returns the file name.
func (m *Manager) backup(ctx context.Context, j *Job, s Site, kind string) (string, error) {
	st, ok := m.run.(Streamer)
	if !ok {
		return "", errors.New("backup is not available")
	}
	dir := m.backupDir(s.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	need := m.siteBytes(s.ID)
	if free := freeBytes(m.cfg.Data); free >= 0 && free < need+(256<<20) {
		return "", fmt.Errorf("%w: not enough free disk space for a backup (%d MB free, about %d MB needed)", ErrBadInput, free>>20, (need+(256<<20))>>20)
	}
	now := m.now()
	name := fmt.Sprintf("%s-%s.tar.gz", kind, now.In(wib).Format("20060102-150405"))
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); err == nil {
		return "", fmt.Errorf("%w: a backup was made this second; try again", ErrBadInput)
	}

	step := "copy" // the panel shows these step names; a restore's own safety copy has its own
	if kind == "before-restore" {
		step = "safety"
	}
	// 1. the database, into a private temporary file (its size must be known for the archive header)
	var sqlf *os.File
	var dbSize int64
	if s.HasDB() {
		j.Step(step + " database")
		sqlPath := filepath.Join(dir, ".db-"+randHex(6)+".sql")
		f, err := os.OpenFile(sqlPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", err
		}
		defer os.Remove(sqlPath)
		defer f.Close()
		sqlf = f
		bw := bufio.NewWriterSize(sqlf, 1<<20)
		sdir := m.dir(s.ID)
		args := append([]string{"compose", "--project-directory", sdir, "-p", Project(s.ID), "-f", filepath.Join(sdir, "compose.yml")},
			m.dumpArgs(s, true)...)
		errOut, err := st.DockerTo(ctx, sdir, nil, bw, args...)
		if err = errors.Join(err, bw.Flush()); err != nil {
			if len(errOut) > 0 {
				return "", fmt.Errorf("database copy failed: %w", sqlErr(s, errOut))
			}
			return "", fmt.Errorf("database copy failed: %w", err)
		}
		dbInfo, err := sqlf.Stat()
		if err != nil {
			return "", err
		}
		dbSize = dbInfo.Size()
		if _, err := sqlf.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
	}

	// 2. the archive
	j.Step(step + " files")
	part := final + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	kept := false
	defer func() {
		if !kept {
			os.Remove(part)
		}
	}()
	gz, _ := gzip.NewWriterLevel(out, gzip.BestSpeed)
	tw := tar.NewWriter(gz)
	man := Manifest{Format: BackupFormat, Site: s.ID, Domain: s.Domain, Server: s.Server, PHP: s.PHP, Kind: kind,
		Created: now.Unix(), DBBytes: dbSize, Complete: true}
	files, bytes, err := m.countFiles(s.ID)
	if err != nil {
		out.Close()
		return "", err
	}
	man.Files, man.Bytes = files, bytes
	mb, _ := json.MarshalIndent(man, "", "  ")
	sb, _ := json.MarshalIndent(s, "", "  ")
	for _, x := range []struct {
		name string
		body []byte
	}{{"manifest.json", mb}, {"site.json", sb}} {
		if err := tw.WriteHeader(&tar.Header{Name: x.name, Mode: 0o600, Size: int64(len(x.body)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			out.Close()
			return "", err
		}
		if _, err := tw.Write(x.body); err != nil {
			out.Close()
			return "", err
		}
	}
	if sqlf != nil {
		if err := tw.WriteHeader(&tar.Header{Name: "database.sql", Mode: 0o600, Size: dbSize, ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			out.Close()
			return "", err
		}
		if _, err := io.Copy(tw, sqlf); err != nil {
			out.Close()
			return "", err
		}
	}
	if err := m.tarFiles(ctx, s.ID, tw); err != nil {
		out.Close()
		return "", fmt.Errorf("file copy failed: %w", err)
	}
	if err := errors.Join(tw.Close(), gz.Close(), out.Sync(), out.Close()); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	kept = true
	_ = os.Chtimes(final, now, now)
	switch kind {
	case "daily":
		m.prune(s.ID, "daily", s.keep())
	case "before-restore", "before-update":
		m.prune(s.ID, kind, keepSafety)
	}
	return name, nil
}

func (m *Manager) countFiles(id string) (int64, int64, error) {
	r, err := m.openWP(id)
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()
	var n, b int64
	err = fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		n++
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				b += fi.Size()
			}
		}
		return nil
	})
	return n, b, err
}

// tarFiles adds the website folder under wp/. It walks inside an os.Root: links are stored as links, never followed.
func (m *Manager) tarFiles(ctx context.Context, id string, tw *tar.Writer) error {
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	return fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(path.Base(p), ".np-upload-") {
			return nil
		}
		fi, err := r.Lstat(p)
		if err != nil {
			return err
		}
		name := "wp"
		if p != "." {
			name = "wp/" + p
		}
		h := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime(), Format: tar.FormatPAX}
		if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
			h.Uid, h.Gid = int(sys.Uid), int(sys.Gid)
		}
		switch {
		case fi.IsDir():
			h.Typeflag, h.Name = tar.TypeDir, name+"/"
		case fi.Mode()&fs.ModeSymlink != 0:
			t, err := r.Readlink(p)
			if err != nil {
				return err
			}
			h.Typeflag, h.Linkname = tar.TypeSymlink, t
		case fi.Mode().IsRegular():
			h.Typeflag, h.Size = tar.TypeReg, fi.Size()
		default:
			return nil // sockets, devices, pipes: not website content
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			return nil
		}
		f, err := r.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := io.Copy(tw, io.LimitReader(f, h.Size))
		if err == nil && n != h.Size {
			err = fmt.Errorf("%s changed size during the backup", p)
		}
		return err
	})
}

// StartRestore restores a backup: what is "all", "files" or "database".
func (m *Manager) StartRestore(id, name, what string) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !s.HasDB() {
		if what == "database" {
			return "", fmt.Errorf("%w: this website has no database", ErrBadInput)
		}
		if what == "all" {
			what = "files"
		}
	}
	if err := backupName(name); err != nil {
		return "", err
	}
	if what != "all" && what != "files" && what != "database" {
		return "", fmt.Errorf("%w: restore all, files or database", ErrBadInput)
	}
	src := filepath.Join(m.backupDir(id), name)
	man, err := readManifest(src)
	if err != nil {
		return "", err
	}
	if man.Site != id {
		return "", fmt.Errorf("%w: this backup belongs to website %s", ErrBadInput, man.Site)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("restore", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		safe, err := m.backup(ctx, j, s, "before-restore")
		if err != nil {
			j.Fail("the safety backup before restore failed, nothing was changed: " + err.Error())
			return
		}
		if err := m.restore(ctx, j, s, src, what); err != nil {
			j.Fail(err.Error() + " — the website can be put back with the backup " + safe)
			return
		}
		j.Step("done " + safe)
		j.Done("")
	}()
	return j.ID, nil
}

// resetSQL empties the site database before a restore. name is the site's own database name, never input.
func resetSQL(name string) string {
	return "DROP DATABASE IF EXISTS " + name + ";\nCREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_520_ci;\n"
}

// readManifest reads and checks manifest.json, the first entry of a backup.
func readManifest(p string) (Manifest, error) {
	f, err := os.Open(p)
	if err != nil {
		return Manifest{}, ErrNotFound
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: not a backup archive", ErrBadInput)
	}
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" || h.Size > 64<<10 {
		return Manifest{}, fmt.Errorf("%w: not a Nostek Panel backup", ErrBadInput)
	}
	var man Manifest
	if err := json.NewDecoder(io.LimitReader(tr, 64<<10)).Decode(&man); err != nil || man.Format != BackupFormat || !man.Complete {
		return Manifest{}, fmt.Errorf("%w: not a Nostek Panel backup (format)", ErrBadInput)
	}
	return man, nil
}

// unsafeEntry refuses names that leave the wp/ folder.
func unsafeEntry(name string) bool {
	if !strings.HasPrefix(name, "wp/") && name != "wp/" {
		return true
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// restore applies an archive; the caller holds m.mu and has a safety backup.
func (m *Manager) restore(ctx context.Context, j *Job, s Site, src, what string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: not a backup archive", ErrBadInput)
	}
	tr := tar.NewReader(gz)
	sdir := m.dir(s.ID)
	var stage string
	var root *os.Root
	defer func() {
		if root != nil {
			root.Close()
		}
		if stage != "" {
			_ = os.RemoveAll(stage)
		}
	}()
	sawDB := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the backup is damaged: %w", err)
		}
		switch {
		case h.Name == "manifest.json" || h.Name == "site.json":
			continue
		case h.Name == "database.sql":
			sawDB = true
			if what == "files" {
				continue
			}
			j.Step("restore database")
			// start from an empty database, so tables made after the backup do not stay behind
			reset, rargs := m.resetArgs(s)
			if out, err := m.compose(ctx, s.ID, strings.NewReader(reset), rargs...); err != nil {
				return fmt.Errorf("database restore failed: %w", sqlErr(s, out))
			}
			out, err := m.compose(ctx, s.ID, tr, m.loadArgs(s)...)
			if err != nil {
				return fmt.Errorf("database restore failed: %w", sqlErr(s, out))
			}
			continue
		}
		if what == "database" {
			break
		}
		if unsafeEntry(h.Name) {
			return fmt.Errorf("%w: the backup holds a path outside the website: %q", ErrBadInput, h.Name)
		}
		if root == nil {
			j.Step("restore files")
			stage = filepath.Join(sdir, "wp.restore-"+randHex(4))
			if err := os.Mkdir(stage, 0o755); err != nil {
				return err
			}
			if root, err = os.OpenRoot(stage); err != nil {
				return err
			}
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(h.Name, "wp/"), "/")
		if rel == "" || h.Name == "wp/" {
			continue
		}
		if err := extract(root, rel, h, tr); err != nil {
			return fmt.Errorf("file restore failed at %s: %w", rel, err)
		}
	}
	if !sawDB && what != "files" {
		return fmt.Errorf("%w: the backup has no database", ErrBadInput)
	}
	if what == "database" {
		return nil
	}
	if root == nil {
		return fmt.Errorf("%w: the backup has no website files", ErrBadInput)
	}
	// swap the folders: the running containers keep the old folder mounted, so they are recreated on the new one
	j.Step("switch files")
	stop := []string{"stop", "web"} // the containers that mount the website folder
	switch {
	case s.IsRuntime(): // Node.js, Go, Python: one app container, no web server
		stop = []string{"stop", "app"}
	case s.Server == "nginx":
		stop = append(stop, "php")
	}
	if out, err := m.compose(ctx, s.ID, nil, stop...); err != nil {
		return fmt.Errorf("stop failed: %s", tail(out))
	}
	old := filepath.Join(sdir, "wp.old-"+randHex(4))
	wp := m.wpDir(s.ID)
	if err := os.Rename(wp, old); err != nil {
		return err
	}
	if err := os.Rename(stage, wp); err != nil {
		_ = os.Rename(old, wp)
		return err
	}
	stage = ""
	if out, err := m.compose(ctx, s.ID, nil, "up", "-d", "--force-recreate", "--wait", "--wait-timeout", "180"); err != nil {
		return fmt.Errorf("start after restore failed: %s", tail(out))
	}
	return os.RemoveAll(old)
}

// extract writes one archive entry inside root with the archived owner; permissions never carry setuid/setgid.
func extract(root *os.Root, rel string, h *tar.Header, r io.Reader) error {
	if d := path.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	mode := fs.FileMode(h.Mode) & 0o777
	switch h.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(rel, 0o755); err != nil {
			return err
		}
	case tar.TypeSymlink:
		if err := root.Symlink(h.Linkname, rel); err != nil {
			return err
		}
		return root.Lchown(rel, h.Uid, h.Gid)
	case tar.TypeReg:
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, r)
		if err = errors.Join(err, f.Close()); err != nil {
			return err
		}
		if n != h.Size {
			return errors.New("short file")
		}
	default:
		return nil
	}
	if err := root.Chown(rel, h.Uid, h.Gid); err != nil {
		return err
	}
	if err := root.Chmod(rel, mode); err != nil {
		return err
	}
	return root.Chtimes(rel, h.ModTime, h.ModTime)
}

// RunDailyBackups makes the daily backup of every site that asked for one, once a day after DailyHour WIB.
func (m *Manager) RunDailyBackups(ctx context.Context) []string {
	now := m.now().In(wib)
	if now.Hour() < DailyHour {
		return nil
	}
	list, err := m.List()
	if err != nil {
		return []string{err.Error()}
	}
	today := now.Format("20060102")
	var errs []string
	for _, s := range list {
		if !s.BackupDaily || s.Phase != "ready" {
			continue
		}
		b, err := m.ListBackups(s.ID)
		if err != nil {
			continue
		}
		done := false
		for _, x := range b.Backups {
			if x.Kind == "daily" && strings.HasPrefix(x.Name, "daily-"+today) {
				done = true
			}
		}
		if done {
			continue
		}
		m.mu.Lock()
		j := m.jobs.New("backup", s.ID)
		c, cancel := context.WithTimeout(ctx, 3*time.Hour)
		name, err := m.backup(c, j, s, "daily")
		cancel()
		m.mu.Unlock()
		if err != nil {
			j.Fail(err.Error())
			errs = append(errs, s.ID+": "+err.Error())
			continue
		}
		j.Step("done " + name)
		j.Done("")
	}
	return errs
}

// BackupLoop calls RunDailyBackups every BackupEvery until ctx ends.
func (m *Manager) BackupLoop(ctx context.Context, logf func(string, ...any)) {
	t := time.NewTicker(BackupEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, e := range m.RunDailyBackups(ctx) {
				logf("daily backup: %s", e)
			}
		}
	}
}
