package sites

// File manager (SITE-02). Every operation works inside the site's WordPress folder through os.Root, so no path,
// symbolic link or ".." can reach anything outside it (not the database files, not the secrets, not the host).
// Files the agent creates belong to www-data (33:33), like the files WordPress writes itself.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxUpload is the largest file one upload or save may write.
	MaxUpload = 256 << 20
	// MaxEdit is the largest file the panel opens in its text editor.
	MaxEdit = 2 << 20
	// MaxList is the most entries one folder listing returns.
	MaxList = 5000
	// MaxUnzip limits what one zip extraction may write.
	MaxUnzip      = 2 << 30
	maxUnzipFiles = 20000
	wwwData       = 33
)

// ErrExists is returned when a target name is already taken.
var ErrExists = errors.New("a file or folder with that name already exists")

// ErrTooBig is returned when a body is larger than allowed.
var ErrTooBig = errors.New("file is too large")

// Entry is one row of a folder listing.
type Entry struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // dir | file | link | other
	Size     int64  `json:"size"`
	Mode     string `json:"mode"` // e.g. 0644
	Modified int64  `json:"modified"`
	Target   string `json:"target,omitempty"` // symbolic link target, as stored
}

// Listing is one folder.
type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// CleanPath turns a panel path ("/", "/wp-content/x") into a root-relative name ("." or "wp-content/x").
func CleanPath(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	if len(p) > 1024 || strings.ContainsRune(p, 0) || !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: path must start with / and be at most 1024 characters", ErrBadInput)
	}
	c := strings.TrimPrefix(path.Clean(p), "/")
	if c == "" {
		return ".", nil
	}
	for _, part := range strings.Split(c, "/") {
		if len(part) > 255 {
			return "", fmt.Errorf("%w: a name is longer than 255 characters", ErrBadInput)
		}
	}
	return c, nil
}

// shown is the panel form of a root-relative name.
func shown(rel string) string {
	if rel == "." {
		return "/"
	}
	return "/" + rel
}

// validName checks a single file or folder name typed by the user.
func validName(n string) error {
	if n == "" || n == "." || n == ".." || len(n) > 255 || strings.ContainsAny(n, "/\x00") {
		return fmt.Errorf("%w: name is not valid", ErrBadInput)
	}
	return nil
}

// uid is the owner of files the agent writes: www-data, or Config.FileUID (tests that do not run as root).
func (m *Manager) uid() int {
	if m.cfg.FileUID > 0 {
		return m.cfg.FileUID
	}
	return wwwData
}

// StatFile reports one file or folder.
func (m *Manager) StatFile(id, p string) (Entry, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	r, err := m.openWP(id)
	if err != nil {
		return Entry{}, err
	}
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		return Entry{}, notExist(err)
	}
	return entryOf(path.Base("/"+rel), fi, ""), nil
}

func (m *Manager) wpDir(id string) string { return filepath.Join(m.dir(id), "wp") }

func (m *Manager) filesTrashDir(id string) string { return filepath.Join(m.dir(id), "files-trash") }

func (m *Manager) openWP(id string) (*os.Root, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(m.wpDir(id))
	if err != nil {
		return nil, fmt.Errorf("website folder not available: %w", err)
	}
	return r, nil
}

func entryOf(name string, fi fs.FileInfo, target string) Entry {
	t := "other"
	switch {
	case fi.Mode().IsDir():
		t = "dir"
	case fi.Mode().IsRegular():
		t = "file"
	case fi.Mode()&fs.ModeSymlink != 0:
		t = "link"
	}
	return Entry{Name: name, Type: t, Size: fi.Size(), Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), Modified: fi.ModTime().Unix(), Target: target}
}

func notExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: no such file or folder", ErrBadInput)
	}
	return err
}

// ListFiles reads one folder: folders first, then files, by name.
func (m *Manager) ListFiles(id, p string) (Listing, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return Listing{}, err
	}
	r, err := m.openWP(id)
	if err != nil {
		return Listing{}, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return Listing{}, notExist(err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.IsDir() {
		return Listing{}, fmt.Errorf("%w: not a folder", ErrBadInput)
	}
	names, err := f.Readdirnames(MaxList + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Listing{}, err
	}
	out := Listing{Path: shown(rel), Entries: []Entry{}}
	if len(names) > MaxList {
		names, out.Truncated = names[:MaxList], true
	}
	for _, n := range names {
		child := path.Join(rel, n)
		fi, err := r.Lstat(child)
		if err != nil {
			continue
		}
		target := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			target, _ = r.Readlink(child)
		}
		out.Entries = append(out.Entries, entryOf(n, fi, target))
	}
	sort.Slice(out.Entries, func(a, b int) bool {
		da, db := out.Entries[a].Type == "dir", out.Entries[b].Type == "dir"
		if da != db {
			return da
		}
		return strings.ToLower(out.Entries[a].Name) < strings.ToLower(out.Entries[b].Name)
	})
	return out, nil
}

// OpenFile opens a regular file for download. The caller closes it.
func (m *Manager) OpenFile(id, p string) (*os.File, fs.FileInfo, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return nil, nil, err
	}
	r, err := m.openWP(id)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, nil, notExist(err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%w: not a file", ErrBadInput)
	}
	return f, fi, nil
}

// WriteFile creates or (with overwrite) replaces a file from body, atomically: the old file stays until the new
// one is complete. A replaced file keeps its permissions.
func (m *Manager) WriteFile(id, p string, body io.Reader, overwrite bool) (Entry, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return Entry{}, err
	}
	if rel == "." {
		return Entry{}, fmt.Errorf("%w: a file name is needed", ErrBadInput)
	}
	if err := validName(path.Base(rel)); err != nil {
		return Entry{}, err
	}
	r, err := m.openWP(id)
	if err != nil {
		return Entry{}, err
	}
	defer r.Close()
	if fi, err := r.Stat(path.Dir(rel)); err != nil || !fi.IsDir() {
		return Entry{}, fmt.Errorf("%w: folder %s does not exist", ErrBadInput, shown(path.Dir(rel)))
	}
	mode := os.FileMode(0o644)
	if fi, err := r.Lstat(rel); err == nil {
		if !overwrite {
			return Entry{}, ErrExists
		}
		if !fi.Mode().IsRegular() {
			return Entry{}, fmt.Errorf("%w: only a regular file can be replaced", ErrBadInput)
		}
		mode = fi.Mode().Perm()
	}
	tmp := path.Join(path.Dir(rel), ".np-upload-"+randHex(6))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Entry{}, err
	}
	n, err := io.Copy(f, io.LimitReader(body, MaxUpload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxUpload {
		err = ErrTooBig
	}
	if err == nil {
		err = errors.Join(r.Chown(tmp, m.uid(), m.uid()), r.Chmod(tmp, mode))
	}
	if err == nil {
		err = r.Rename(tmp, rel)
	}
	if err != nil {
		_ = r.Remove(tmp)
		return Entry{}, err
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return Entry{}, err
	}
	return entryOf(path.Base(rel), fi, ""), nil
}

// Mkdir creates one folder.
func (m *Manager) Mkdir(id, p string) error {
	rel, err := CleanPath(p)
	if err != nil {
		return err
	}
	if rel == "." {
		return ErrExists
	}
	if err := validName(path.Base(rel)); err != nil {
		return err
	}
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Mkdir(rel, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return notExist(err)
	}
	return r.Chown(rel, m.uid(), m.uid())
}

// Rename renames or moves a file or folder inside the website folder. The target must not exist.
func (m *Manager) Rename(id, from, to string) error {
	a, err := CleanPath(from)
	if err != nil {
		return err
	}
	b, err := CleanPath(to)
	if err != nil {
		return err
	}
	if a == "." || b == "." || a == b {
		return fmt.Errorf("%w: choose another name", ErrBadInput)
	}
	if err := validName(path.Base(b)); err != nil {
		return err
	}
	if strings.HasPrefix(b+"/", a+"/") {
		return fmt.Errorf("%w: a folder cannot move into itself", ErrBadInput)
	}
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(a); err != nil {
		return notExist(err)
	}
	if _, err := r.Lstat(b); err == nil {
		return ErrExists
	}
	return notExist(r.Rename(a, b))
}

// Chmod sets permissions (only the rwx bits; no setuid, setgid or sticky).
func (m *Manager) Chmod(id, p, mode string) error {
	rel, err := CleanPath(p)
	if err != nil {
		return err
	}
	v, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || len(mode) < 3 || len(mode) > 4 || v > 0o777 {
		return fmt.Errorf("%w: permissions must be like 644 or 755", ErrBadInput)
	}
	if v&0o600 != 0o600 && v&0o700 != 0o700 {
		// the owner (www-data) must keep read and write, or WordPress breaks itself
		return fmt.Errorf("%w: the owner must keep read and write (6 or 7 first)", ErrBadInput)
	}
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		return notExist(err)
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: only files and folders", ErrBadInput)
	}
	return r.Chmod(rel, os.FileMode(v))
}

// FileTrash is one deleted file or folder, restorable for TrashDays.
type FileTrash struct {
	Entry   string `json:"entry"`
	Path    string `json:"path"`
	Type    string `json:"type"`
	Deleted int64  `json:"deleted"`
	Purge   int64  `json:"purge"`
}

func validTrashEntry(e string) bool {
	if len(e) < 12 || len(e) > 40 {
		return false
	}
	for _, c := range e {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c == '-') {
			return false
		}
	}
	return true
}

// renameAt moves one name between two open folders without following a symbolic link at the name itself.
func renameAt(from *os.File, fromName string, to *os.File, toName string) error {
	return syscall.Renameat(int(from.Fd()), fromName, int(to.Fd()), toName)
}

// DeleteFile moves a file or folder to the website's file trash (kept TrashDays, then removed by purge).
func (m *Manager) DeleteFile(id, p string) (FileTrash, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return FileTrash{}, err
	}
	if rel == "." {
		return FileTrash{}, fmt.Errorf("%w: the website folder itself cannot be deleted here", ErrBadInput)
	}
	r, err := m.openWP(id)
	if err != nil {
		return FileTrash{}, err
	}
	defer r.Close()
	fi, err := r.Lstat(rel)
	if err != nil {
		return FileTrash{}, notExist(err)
	}
	parent, err := r.Open(path.Dir(rel))
	if err != nil {
		return FileTrash{}, notExist(err)
	}
	defer parent.Close()
	if err := os.MkdirAll(m.filesTrashDir(id), 0o700); err != nil {
		return FileTrash{}, err
	}
	now := m.now()
	t := FileTrash{Entry: fmt.Sprintf("%d-%s", now.Unix(), randHex(4)), Path: shown(rel), Type: entryOf("", fi, "").Type,
		Deleted: now.Unix(), Purge: now.Add(TrashDays * 24 * time.Hour).Unix()}
	dir := filepath.Join(m.filesTrashDir(id), t.Entry)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return FileTrash{}, err
	}
	meta, _ := json.Marshal(t)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600); err != nil {
		os.RemoveAll(dir)
		return FileTrash{}, err
	}
	td, err := os.Open(dir)
	if err != nil {
		os.RemoveAll(dir)
		return FileTrash{}, err
	}
	defer td.Close()
	if err := renameAt(parent, path.Base(rel), td, "item"); err != nil {
		os.RemoveAll(dir)
		return FileTrash{}, err
	}
	return t, nil
}

// FilesTrash lists the website's deleted files, newest first.
func (m *Manager) FilesTrash(id string) ([]FileTrash, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(m.filesTrashDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return []FileTrash{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []FileTrash{}
	for _, e := range ents {
		if !validTrashEntry(e.Name()) {
			continue
		}
		var t FileTrash
		b, err := os.ReadFile(filepath.Join(m.filesTrashDir(id), e.Name(), "meta.json"))
		if err != nil || json.Unmarshal(b, &t) != nil || t.Entry != e.Name() {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Deleted > out[b].Deleted })
	return out, nil
}

// RestoreFile puts a deleted file or folder back at its old place (which must be free).
func (m *Manager) RestoreFile(id, entry string) (string, error) {
	if !validTrashEntry(entry) {
		return "", fmt.Errorf("%w: unknown trash entry", ErrBadInput)
	}
	list, err := m.FilesTrash(id)
	if err != nil {
		return "", err
	}
	var t *FileTrash
	for i := range list {
		if list[i].Entry == entry {
			t = &list[i]
		}
	}
	if t == nil {
		return "", fmt.Errorf("%w: unknown trash entry", ErrBadInput)
	}
	rel, err := CleanPath(t.Path)
	if err != nil || rel == "." {
		return "", fmt.Errorf("%w: trash entry is damaged", ErrBadInput)
	}
	r, err := m.openWP(id)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if _, err := r.Lstat(rel); err == nil {
		return "", ErrExists
	}
	parent, err := r.Open(path.Dir(rel))
	if err != nil {
		return "", fmt.Errorf("%w: folder %s no longer exists", ErrBadInput, shown(path.Dir(rel)))
	}
	defer parent.Close()
	dir := filepath.Join(m.filesTrashDir(id), entry)
	td, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer td.Close()
	if err := renameAt(td, "item", parent, path.Base(rel)); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	return t.Path, nil
}

// purgeFiles removes file-trash entries older than TrashDays from every site.
func (m *Manager) purgeFiles() []string {
	var gone []string
	sites, _ := m.List()
	now := m.now().Unix()
	for _, s := range sites {
		list, _ := m.FilesTrash(s.ID)
		for _, t := range list {
			if t.Purge > now {
				continue
			}
			if os.RemoveAll(filepath.Join(m.filesTrashDir(s.ID), t.Entry)) == nil {
				gone = append(gone, s.ID+":"+t.Path)
			}
		}
	}
	return gone
}

// ZipFolder writes a zip of one folder (or one file) to w. Symbolic links are skipped.
func (m *Manager) ZipFolder(id, p string, w io.Writer) error {
	rel, err := CleanPath(p)
	if err != nil {
		return err
	}
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Stat(rel); err != nil {
		return notExist(err)
	}
	fsys := r.FS()
	zw := zip.NewWriter(w)
	base := path.Dir(rel)
	err = fs.WalkDir(fsys, rel, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || !(d.IsDir() || d.Type().IsRegular()) {
			return nil
		}
		in := name
		if rel != "." && base != "." {
			in = strings.TrimPrefix(name, base+"/")
		}
		if in == "." {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		h.Name = in
		if d.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		h.Method = zip.Deflate
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(out, f)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// Unzip extracts a .zip file into the folder it is in. Existing files with the same name are replaced; symbolic
// links and names that would leave the folder are refused.
func (m *Manager) Unzip(id, p string) (int, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return 0, err
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".zip") {
		return 0, fmt.Errorf("%w: only .zip files can be extracted", ErrBadInput)
	}
	r, err := m.openWP(id)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return 0, notExist(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("%w: not a file", ErrBadInput)
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return 0, fmt.Errorf("%w: not a valid zip file", ErrBadInput)
	}
	if len(zr.File) > maxUnzipFiles {
		return 0, fmt.Errorf("%w: the zip has more than %d entries", ErrBadInput, maxUnzipFiles)
	}
	dest := path.Dir(rel)
	var budget int64 = MaxUnzip
	n := 0
	for _, zf := range zr.File {
		name := strings.TrimSuffix(zf.Name, "/")
		if name == "" {
			continue
		}
		target, err := CleanPath("/" + path.Join(dest, name))
		if err != nil || target == "." || (dest != "." && !strings.HasPrefix(target, dest+"/")) || unsafeZipName(zf.Name) {
			return n, fmt.Errorf("%w: the zip has an unsafe name %q", ErrBadInput, zf.Name)
		}
		mode := zf.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return n, fmt.Errorf("%w: the zip has a symbolic link %q", ErrBadInput, zf.Name)
		case zf.FileInfo().IsDir():
			if err := r.MkdirAll(target, 0o755); err != nil {
				return n, err
			}
			_ = r.Chown(target, m.uid(), m.uid())
			continue
		}
		if err := r.MkdirAll(path.Dir(target), 0o755); err != nil {
			return n, err
		}
		if fi, err := r.Lstat(target); err == nil && !fi.Mode().IsRegular() {
			return n, fmt.Errorf("%w: %s exists and is not a file", ErrBadInput, shown(target))
		}
		in, err := zf.Open()
		if err != nil {
			return n, fmt.Errorf("%w: %s cannot be read from the zip", ErrBadInput, zf.Name)
		}
		perm := os.FileMode(0o644)
		if mode.Perm()&0o100 != 0 {
			perm = 0o755
		}
		out, err := r.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			in.Close()
			return n, err
		}
		w, err := io.Copy(out, io.LimitReader(in, budget+1))
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, fmt.Errorf("%s: %w", shown(target), err)
		}
		budget -= w
		if budget < 0 {
			return n, fmt.Errorf("%w: the zip unpacks to more than %d GB", ErrBadInput, MaxUnzip>>30)
		}
		_ = r.Chown(target, m.uid(), m.uid())
		_ = r.Chmod(target, perm)
		n++
	}
	return n, nil
}

func unsafeZipName(n string) bool {
	if strings.HasPrefix(n, "/") || strings.Contains(n, `\`) {
		return true
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
