package sites

// Server backups (BAK-02): one sealed file with the panel and every website, to move a whole server to a new VPS.
//
// The file is a tar sealed with npbk (a passphrase, never stored):
//
//	panel/panel.db               a consistent copy the panel made just before (SQLite VACUUM INTO)
//	panel/alerts.key             the key that seals the alert secrets
//	sites/<id>/site.tar          the website folder without its files, database folder and backups (site.json,
//	                             compose and config files, secrets/, deploy keys, SFTP accounts, password files)
//	sites/<id>/backup.tar.gz     a fresh BAK-01 backup (files and database)
//	manifest.json                last: what is inside, with complete = true
//
// A restore reads the whole file once to check it (passphrase, every chunk, every entry the manifest names) before
// anything is changed, then a second time to put it in place.

import (
	"archive/tar"
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
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/npbk"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const (
	// ServerBackupFormat names the layout of a server backup.
	ServerBackupFormat = "nostek-server-backup/1"
	// ServerBackupKeep is how many server backups are kept.
	ServerBackupKeep = 3
	// PanelCopy is the file the panel writes into its data folder just before it asks for a server backup.
	PanelCopy = "server-backup.db"
	// PanelUID owns the panel's data folder (the panel image runs as this user).
	PanelUID = 65532
	// panelCopyAge is how old the panel's copy may be when the backup starts.
	panelCopyAge = 10 * time.Minute
)

var serverBackupRe = regexp.MustCompile(`^nostek-server-[0-9]{8}-[0-9]{6}\.npbk$`)

// ServerSite is one website in a server backup.
type ServerSite struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	App     string `json:"app"`
	Engine  string `json:"engine"` // docker | native
	Bytes   int64  `json:"bytes"`  // size of its backup in the file
	Created int64  `json:"created"`
}

// ServerManifest is manifest.json of a server backup.
type ServerManifest struct {
	Format   string       `json:"format"`
	Created  int64        `json:"created"`
	Host     string       `json:"host"`
	Agent    string       `json:"agent"`
	Panel    string       `json:"panel"`
	Sites    []ServerSite `json:"sites"`
	Skipped  []string     `json:"skipped,omitempty"` // websites not ready when the backup was made ("id: reason")
	AlertKey bool         `json:"alert_key"`
	Complete bool         `json:"complete"`
}

// ServerBackup is one server backup file.
type ServerBackup struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// ServerBackups is the server backup page.
type ServerBackups struct {
	Backups []ServerBackup `json:"backups"`
	Free    int64          `json:"free"` // bytes free on the backup disk
	Need    int64          `json:"need"` // about how much a new backup needs
	Job     *JobView       `json:"job,omitempty"`
}

func (m *Manager) serverBackupDir() string { return filepath.Join(m.cfg.Data, "server-backups") }

func (m *Manager) panelDir() string {
	if m.cfg.PanelData != "" {
		return m.cfg.PanelData
	}
	return filepath.Join(m.cfg.Data, "panel")
}

func (m *Manager) serverJob() *JobView {
	for _, v := range m.jobs.List() {
		if v.Kind == "server-backup" && v.State == "running" {
			v := v
			return &v
		}
	}
	return nil
}

// serverNeed is about how much disk a server backup needs: the websites plus 10 %.
func (m *Manager) serverNeed() int64 {
	list, _ := m.List()
	var n int64
	for _, s := range list {
		n += m.siteBytes(s.ID)
	}
	return n + n/10 + 64<<20
}

// ListServerBackups lists the server backups, newest first.
func (m *Manager) ListServerBackups() (ServerBackups, error) {
	b := ServerBackups{Backups: []ServerBackup{}, Free: freeBytes(m.cfg.Data), Need: m.serverNeed(), Job: m.serverJob()}
	ents, err := os.ReadDir(m.serverBackupDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	for _, e := range ents {
		if !serverBackupRe.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		b.Backups = append(b.Backups, ServerBackup{Name: e.Name(), Size: fi.Size(), Created: fi.ModTime().Unix()})
	}
	sort.Slice(b.Backups, func(i, j int) bool { return b.Backups[i].Name > b.Backups[j].Name })
	return b, nil
}

// OpenServerBackup opens a server backup for download.
func (m *Manager) OpenServerBackup(name string) (*os.File, fs.FileInfo, error) {
	if !serverBackupRe.MatchString(name) {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(filepath.Join(m.serverBackupDir(), name))
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

// DeleteServerBackup removes one server backup.
func (m *Manager) DeleteServerBackup(name string) error {
	if !serverBackupRe.MatchString(name) {
		return ErrNotFound
	}
	if m.serverJob() != nil {
		return ErrBusy
	}
	if err := os.Remove(filepath.Join(m.serverBackupDir(), name)); err != nil {
		return ErrNotFound
	}
	return nil
}

// StartServerBackup starts a server backup job. The panel has written PanelCopy just before; versions are recorded
// in the manifest. The passphrase is used for this file only and never kept.
func (m *Manager) StartServerBackup(pass, panelVersion, agentVersion string) (string, error) {
	if len([]rune(pass)) < npbk.MinPassphrase {
		return "", fmt.Errorf("%w: %s", ErrBadInput, npbk.ErrShort.Error())
	}
	if len(pass) > 1024 {
		return "", fmt.Errorf("%w: the passphrase is too long", ErrBadInput)
	}
	cp := filepath.Join(m.panelDir(), PanelCopy)
	fi, err := os.Lstat(cp)
	if err != nil || !fi.Mode().IsRegular() || m.now().Sub(fi.ModTime()) > panelCopyAge {
		return "", fmt.Errorf("%w: the panel's copy of its database is missing", ErrBadInput)
	}
	if free, need := freeBytes(m.cfg.Data), m.serverNeed()+fi.Size(); free >= 0 && free < need {
		return "", fmt.Errorf("%w: not enough free disk space for a server backup (%d MB free, about %d MB needed)", ErrBadInput, free>>20, need>>20)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("server-backup", "")
	go func() {
		defer m.mu.Unlock()
		defer os.Remove(cp)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		defer cancel()
		name, err := m.serverBackup(ctx, j, pass, panelVersion, agentVersion)
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Step("done " + name)
		j.Done("")
	}()
	return j.ID, nil
}

// tarFile adds one regular file from disk.
func tarFile(tw *tar.Writer, name, src string, mode int64, at time.Time) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: fi.Size(), ModTime: at, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	n, err := io.Copy(tw, io.LimitReader(f, fi.Size()))
	if err == nil && n != fi.Size() {
		err = fmt.Errorf("%s changed size during the backup", name)
	}
	return err
}

func tarBytes(tw *tar.Writer, name string, b []byte, at time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), ModTime: at, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// siteSkip leaves out of site.tar what a fresh BAK-01 backup carries or what is rebuilt: the website files, the raw
// database folder, the backups and old releases, and work folders.
func siteSkip(p string) bool {
	top := strings.SplitN(p, "/", 2)[0]
	switch top {
	case "wp", "db", "backups", "db-backups", "static", "migrate", "files-trash", "tmp", "logs":
		return true
	}
	for _, pre := range []string{"wp.restore-", "wp.old-", "upload-", "dump-", ".db-"} {
		if strings.HasPrefix(top, pre) {
			return true
		}
	}
	return p == "deploy/logs" || strings.HasPrefix(p, "deploy/logs/")
}

// siteTar writes a website's site.tar to a private temporary file and returns its path.
func (m *Manager) siteTar(ctx context.Context, s Site, tmpDir string) (string, error) {
	f, err := os.CreateTemp(tmpDir, ".site-*.tar")
	if err != nil {
		return "", err
	}
	p := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(p)
		}
	}()
	r, err := os.OpenRoot(m.dir(s.ID))
	if err != nil {
		return "", err
	}
	defer r.Close()
	tw := tar.NewWriter(f)
	if err := tarTree(ctx, r, "site", siteSkip, tw); err != nil {
		return "", err
	}
	now := m.now()
	// the SFTP accounts and protected-folder passwords live outside the website folder
	if _, err := os.Stat(m.sftpFileOf(s.ID)); err == nil {
		if err := tarFile(tw, "site/"+sftpTrashedFile, m.sftpFileOf(s.ID), 0o600, now); err != nil {
			return "", err
		}
	}
	for _, pf := range s.Protected {
		src := m.authFile(s.ID, pf.Path)
		if _, err := os.Stat(src); err == nil {
			if err := tarFile(tw, "auth/"+authName(s.ID, pf.Path), src, 0o644, now); err != nil {
				return "", err
			}
		}
	}
	if err := errors.Join(tw.Close(), f.Sync()); err != nil {
		return "", err
	}
	ok = true
	return p, nil
}

func (m *Manager) serverBackup(ctx context.Context, j *Job, pass, panelVersion, agentVersion string) (string, error) {
	dir := m.serverBackupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	now := m.now()
	name := "nostek-server-" + now.In(wib).Format("20060102-150405") + ".npbk"
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); err == nil {
		return "", fmt.Errorf("%w: a server backup was made this second; try again", ErrBadInput)
	}
	part := final + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	kept := false
	defer func() {
		out.Close()
		if !kept {
			os.Remove(part)
		}
	}()
	j.Step("seal")
	sw, err := npbk.NewWriter(out, pass)
	if err != nil {
		return "", err
	}
	tw := tar.NewWriter(sw)
	host, _ := os.Hostname()
	man := ServerManifest{Format: ServerBackupFormat, Created: now.Unix(), Host: host, Agent: agentVersion, Panel: panelVersion, Sites: []ServerSite{}}

	j.Step("panel")
	if err := tarFile(tw, "panel/panel.db", filepath.Join(m.panelDir(), PanelCopy), 0o600, now); err != nil {
		return "", fmt.Errorf("panel database: %w", err)
	}
	if _, err := os.Stat(filepath.Join(m.panelDir(), "alerts.key")); err == nil {
		if err := tarFile(tw, "panel/alerts.key", filepath.Join(m.panelDir(), "alerts.key"), 0o600, now); err != nil {
			return "", fmt.Errorf("alert key: %w", err)
		}
		man.AlertKey = true
	}

	list, err := m.List()
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Phase != "ready" {
			man.Skipped = append(man.Skipped, s.ID+": not ready ("+s.Phase+")")
			continue
		}
		j.Step("website " + s.ID)
		st, err := m.siteTar(ctx, s, dir)
		if err != nil {
			return "", fmt.Errorf("website %s: %w", s.ID, err)
		}
		err = tarFile(tw, "sites/"+s.ID+"/site.tar", st, 0o600, now)
		os.Remove(st)
		if err != nil {
			return "", fmt.Errorf("website %s: %w", s.ID, err)
		}
		bk, err := m.backup(ctx, j, s, "manual")
		if err != nil {
			return "", fmt.Errorf("website %s: %w", s.ID, err)
		}
		src := filepath.Join(m.backupDir(s.ID), bk)
		fi, _ := os.Stat(src)
		err = tarFile(tw, "sites/"+s.ID+"/backup.tar.gz", src, 0o600, now)
		os.Remove(src) // the copy lives in the server backup; the website's own list stays as it was
		if err != nil {
			return "", fmt.Errorf("website %s: %w", s.ID, err)
		}
		engine := "docker"
		if s.IsNative() {
			engine = "native"
		}
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		man.Sites = append(man.Sites, ServerSite{ID: s.ID, Domain: s.Domain, App: s.AppName(), Engine: engine, Bytes: size, Created: now.Unix()})
	}
	j.Step("finish")
	man.Complete = true
	mb, _ := json.MarshalIndent(man, "", "  ")
	if err := tarBytes(tw, "manifest.json", mb, now); err != nil {
		return "", err
	}
	if err := errors.Join(tw.Close(), sw.Close(), out.Sync(), out.Close()); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	kept = true
	_ = os.Chtimes(final, now, now)
	m.pruneServer()
	return name, nil
}

func (m *Manager) pruneServer() {
	b, err := m.ListServerBackups()
	if err != nil {
		return
	}
	for i, x := range b.Backups {
		if i >= ServerBackupKeep {
			_ = os.Remove(filepath.Join(m.serverBackupDir(), x.Name))
		}
	}
}

// serverEntry checks an entry name of a server backup and returns the website id for sites/ entries.
func serverEntry(name string) (id string, ok bool) {
	switch name {
	case "manifest.json", "panel/panel.db", "panel/alerts.key":
		return "", true
	}
	rest, found := strings.CutPrefix(name, "sites/")
	if !found {
		return "", false
	}
	id, file, found := strings.Cut(rest, "/")
	if !found || spec.ID(id) != nil || (file != "site.tar" && file != "backup.tar.gz") {
		return "", false
	}
	return id, true
}

// CheckServerBackup reads a whole server backup and returns its manifest. It changes nothing.
func CheckServerBackup(file, pass string) (ServerManifest, error) {
	var man ServerManifest
	f, err := os.Open(file)
	if err != nil {
		return man, err
	}
	defer f.Close()
	r, err := npbk.NewReader(f, pass)
	if err != nil {
		return man, err
	}
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	last := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, npbk.ErrPassphrase) || errors.Is(err, npbk.ErrCut) {
				return man, err
			}
			return man, fmt.Errorf("%w: %v", npbk.ErrFormat, err)
		}
		if _, ok := serverEntry(h.Name); !ok || h.Typeflag != tar.TypeReg || seen[h.Name] {
			return man, fmt.Errorf("%w: unexpected entry %q", npbk.ErrFormat, h.Name)
		}
		seen[h.Name], last = true, h.Name
		if h.Name == "manifest.json" {
			if h.Size > 1<<20 {
				return man, fmt.Errorf("%w: manifest too large", npbk.ErrFormat)
			}
			if err := json.NewDecoder(tr).Decode(&man); err != nil {
				return man, fmt.Errorf("%w: manifest: %v", npbk.ErrFormat, err)
			}
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return man, err
		}
	}
	if last != "manifest.json" || man.Format != ServerBackupFormat || !man.Complete || !seen["panel/panel.db"] || man.AlertKey != seen["panel/alerts.key"] {
		return man, fmt.Errorf("%w: the file is incomplete", npbk.ErrFormat)
	}
	ids := map[string]bool{}
	for _, s := range man.Sites {
		if ids[s.ID] || !seen["sites/"+s.ID+"/site.tar"] || !seen["sites/"+s.ID+"/backup.tar.gz"] {
			return man, fmt.Errorf("%w: website %s is incomplete", npbk.ErrFormat, s.ID)
		}
		ids[s.ID] = true
	}
	for n := range seen {
		if id, _ := serverEntry(n); id != "" && !ids[id] {
			return man, fmt.Errorf("%w: website %s is not in the manifest", npbk.ErrFormat, id)
		}
	}
	return man, nil
}

// RestorePanel puts panel.db and alerts.key of a checked server backup into the panel's data folder, owned by
// PanelUID. The panel must be stopped; the files it had are replaced.
func (m *Manager) RestorePanel(file, pass string) error {
	man, err := CheckServerBackup(file, pass)
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := npbk.NewReader(f, pass)
	if err != nil {
		return err
	}
	dir := m.panelDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var dst string
		switch h.Name {
		case "panel/panel.db":
			dst = filepath.Join(dir, "panel.db")
		case "panel/alerts.key":
			dst = filepath.Join(dir, "alerts.key")
		default:
			continue
		}
		tmp := dst + ".restore"
		w, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, tr)
		if err = errors.Join(err, w.Sync(), w.Close(), os.Chown(tmp, m.panelUID(), m.panelUID())); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	// SQLite side files of the database it had would be applied to the restored one
	for _, x := range []string{"panel.db-wal", "panel.db-shm", "panel.db-journal"} {
		_ = os.Remove(filepath.Join(dir, x))
	}
	if !man.AlertKey {
		_ = os.Remove(filepath.Join(dir, "alerts.key"))
	}
	return nil
}

func (m *Manager) panelUID() int {
	if m.cfg.PanelUID != 0 {
		return m.cfg.PanelUID
	}
	return PanelUID
}

// SiteResult is what a restore did with one website.
type SiteResult struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Result string `json:"result"` // restored | skipped | failed
	Note   string `json:"note,omitempty"`
}

// RestoreSites brings back every Docker website of a server backup. A website whose name exists already is
// skipped; a native website is skipped with its BAK-01 backup kept in <data>/server-restore/<id>/ (it needs its
// Linux user, PHP pool and database made first). One failed website does not stop the others (its backup is kept
// too). logf gets one line per step.
func (m *Manager) RestoreSites(ctx context.Context, file, pass string, logf func(string, ...any)) ([]SiteResult, error) {
	man, err := CheckServerBackup(file, pass)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := npbk.NewReader(f, pass)
	if err != nil {
		return nil, err
	}
	work := filepath.Join(m.cfg.Data, "server-restore")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, err
	}
	domains := map[string]string{}
	for _, s := range man.Sites {
		domains[s.ID] = s.Domain
	}
	type state struct {
		SiteResult
		keep bool // keep its BAK-01 backup in work/<id> for a manual restore
	}
	results := map[string]*state{}
	var order []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, _ := serverEntry(h.Name)
		if id == "" {
			continue
		}
		res := results[id]
		if res == nil {
			res = &state{SiteResult: SiteResult{ID: id, Domain: domains[id]}}
			results[id] = res
			order = append(order, id)
		}
		switch path.Base(h.Name) {
		case "site.tar":
			logf("website %s: settings", id)
			if _, err := os.Stat(m.dir(id)); err == nil {
				res.Result, res.Note = "skipped", "a website with this name exists on this server"
				continue
			}
			s, err := m.unpackSite(id, tr)
			if err != nil {
				res.Result, res.Note, res.keep = "failed", err.Error(), true
				continue
			}
			if s.IsNative() {
				// a native website needs its Linux user, PHP pool and database made on this server first
				m.dropUnpacked(s)
				res.Result, res.Note, res.keep = "skipped", "native website: create it again with the same domain, then restore this backup", true
			}
		case "backup.tar.gz":
			if res.Result != "" && !res.keep {
				continue
			}
			keep := filepath.Join(work, id)
			bk := filepath.Join(keep, "backup.tar.gz")
			if err := errors.Join(os.MkdirAll(keep, 0o700), copyOut(bk, tr)); err != nil {
				res.Result, res.Note = "failed", err.Error()
				continue
			}
			if res.Result != "" {
				continue
			}
			logf("website %s: files and database", id)
			if err := m.bringBack(ctx, id, bk); err != nil {
				res.Result, res.Note, res.keep = "failed", err.Error(), true
				continue
			}
			res.Result = "restored"
			_ = os.RemoveAll(keep)
		}
	}
	out := make([]SiteResult, 0, len(order))
	for _, id := range order {
		res := results[id]
		if res.Result == "" {
			res.Result, res.Note = "failed", "incomplete"
		}
		if res.keep {
			res.Note += " (backup kept in " + filepath.Join(work, id, "backup.tar.gz") + ")"
		}
		logf("website %s: %s %s", id, res.Result, res.Note)
		out = append(out, res.SiteResult)
	}
	_ = os.Remove(work) // only when empty
	if err := m.SyncWAF(); err != nil {
		logf("waf files: %v", err)
	}
	if err := m.SyncSFTP(); err != nil {
		logf("sftp files: %v", err)
	}
	return out, nil
}

func copyOut(dst string, r io.Reader) error {
	w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return errors.Join(err, w.Sync(), w.Close())
}

// unpackSite puts site.tar in place as the website folder (with empty wp/ and db/ folders) and its password
// files; the SFTP accounts come back like a restore from the trash.
func (m *Manager) unpackSite(id string, r io.Reader) (Site, error) {
	if err := os.MkdirAll(m.sitesDir(), 0o755); err != nil {
		return Site{}, err
	}
	stage := filepath.Join(m.sitesDir(), ".restore-"+id+"-"+randHex(4))
	if err := os.Mkdir(stage, 0o755); err != nil {
		return Site{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(stage)
		}
	}()
	root, err := os.OpenRoot(stage)
	if err != nil {
		return Site{}, err
	}
	defer root.Close()
	tr := tar.NewReader(r)
	auth := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Site{}, fmt.Errorf("site.tar: %w", err)
		}
		if a, ok := strings.CutPrefix(h.Name, "auth/"); ok {
			if a != authNameBase(a) || !strings.HasPrefix(a, "site-"+id+"-") || h.Size > 1<<20 {
				return Site{}, fmt.Errorf("site.tar: bad entry %q", h.Name)
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return Site{}, err
			}
			auth[a] = b
			continue
		}
		rel, ok := strings.CutPrefix(h.Name, "site/")
		if !ok && h.Name != "site/" {
			return Site{}, fmt.Errorf("site.tar: bad entry %q", h.Name)
		}
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		if strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") || strings.Contains("/"+rel+"/", "/../") {
			return Site{}, fmt.Errorf("site.tar: bad entry %q", h.Name)
		}
		if err := extract(root, rel, h, tr); err != nil {
			return Site{}, fmt.Errorf("site.tar at %s: %w", rel, err)
		}
	}
	s, err := readSite(stage)
	if err != nil || s.ID != id {
		return Site{}, errors.New("site.tar has no settings for this website")
	}
	if err := os.MkdirAll(filepath.Join(stage, "wp"), 0o755); err != nil {
		return s, err
	}
	if s.HasDB() && !s.IsNative() {
		if err := os.MkdirAll(filepath.Join(stage, "db"), 0o755); err != nil {
			return s, err
		}
	}
	if err := os.Rename(stage, m.dir(id)); err != nil {
		return s, err
	}
	done = true
	for name, b := range auth {
		p := filepath.Join(m.cfg.Data, "edge", "dynamic", "auth", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = writeFile(p, b, 0o644)
		}
	}
	m.sftpFromTrash(id)
	return s, nil
}

func authNameBase(a string) string { return filepath.Base(filepath.Clean("/" + a)) }

// dropUnpacked takes back an unpacked website that is not brought back (its folder, password and SFTP files).
func (m *Manager) dropUnpacked(s Site) {
	for _, pf := range s.Protected {
		_ = os.Remove(m.authFile(s.ID, pf.Path))
	}
	_ = os.Remove(m.sftpFileOf(s.ID))
	_ = os.RemoveAll(m.dir(s.ID))
}

// bringBack starts a website from its unpacked folder and restores its BAK-01 backup into it.
func (m *Manager) bringBack(ctx context.Context, id, bk string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	man, err := readManifest(bk)
	if err != nil {
		return err
	}
	if man.Site != id {
		return fmt.Errorf("the backup belongs to website %s", man.Site)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	j := m.jobs.New("restore", id)
	what := "files"
	if s.HasDB() {
		what = "all"
	}
	{
		if svc := buildService(s); svc != "" {
			if out, err := m.compose(ctx, id, nil, "build", "--quiet", svc); err != nil {
				j.Fail("build")
				return fmt.Errorf("build failed: %s", tail(out))
			}
		}
		if out, err := m.compose(ctx, id, nil, "--profile", "tools", "pull", "--quiet", "--policy", "missing", "--ignore-buildable"); err != nil {
			j.Fail("pull")
			return fmt.Errorf("pull images failed: %s", tail(out))
		}
		if s.HasDB() {
			if out, err := m.compose(ctx, id, nil, "up", "-d", "--wait", "--wait-timeout", "300", "db"); err != nil {
				j.Fail("database")
				return fmt.Errorf("database start failed: %s", tail(out))
			}
		}
	}
	if err := m.restore(ctx, j, s, bk, what); err != nil {
		j.Fail(err.Error())
		return err
	}
	j.Done("")
	return nil
}
