package sites

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readySite creates a website with the fake runner and returns its manager, runner and id.
func readySite(t *testing.T) (*Manager, *fakeRunner, string) {
	t.Helper()
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	job, s, err := m.Create(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create: %+v", v)
	}
	return m, f, s.ID
}

func names(l Listing) string {
	var n []string
	for _, e := range l.Entries {
		n = append(n, e.Type+":"+e.Name)
	}
	return strings.Join(n, " ")
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{"/": ".", "": ".", "/a/b/": "a/b", "/a/../b": "b", "/../../etc/passwd": "etc/passwd", "//x//y": "x/y"} {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a/b", "/a\x00b", "/" + strings.Repeat("x", 256), "/" + strings.Repeat("a/", 600)} {
		if _, err := CleanPath(bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
}

func TestFilesWriteListReadRenameChmod(t *testing.T) {
	m, _, id := readySite(t)
	if _, err := m.WriteFile(id, "/hello.txt", strings.NewReader("hi"), false); err != nil {
		t.Fatal(err)
	}
	if err := m.Mkdir(id, "/wp-content"); err != nil {
		t.Fatal(err)
	}
	if err := m.Mkdir(id, "/wp-content"); !errors.Is(err, ErrExists) {
		t.Fatalf("mkdir twice: %v", err)
	}
	if _, err := m.WriteFile(id, "/hello.txt", strings.NewReader("again"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("write without overwrite: %v", err)
	}
	if err := m.Chmod(id, "/hello.txt", "600"); err != nil {
		t.Fatal(err)
	}
	e, err := m.WriteFile(id, "/hello.txt", strings.NewReader("hello world"), true)
	if err != nil || e.Size != 11 || e.Mode != "0600" {
		t.Fatalf("overwrite: %+v %v (mode must be kept)", e, err)
	}
	if _, err := m.WriteFile(id, "/missing/x.txt", strings.NewReader("x"), false); !errors.Is(err, ErrBadInput) {
		t.Fatalf("write into a missing folder: %v", err)
	}
	l, err := m.ListFiles(id, "/")
	if err != nil || names(l) != "dir:wp-content file:hello.txt" || l.Path != "/" {
		t.Fatalf("list: %q %v", names(l), err)
	}
	f, _, err := m.OpenFile(id, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello world" {
		t.Fatalf("read back %q", b)
	}
	if _, _, err := m.OpenFile(id, "/wp-content"); !errors.Is(err, ErrBadInput) {
		t.Fatal("a folder opened as a file")
	}
	if err := m.Rename(id, "/hello.txt", "/wp-content/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := m.Rename(id, "/wp-content", "/wp-content/inside"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("folder moved into itself: %v", err)
	}
	if err := m.Mkdir(id, "/other"); err != nil {
		t.Fatal(err)
	}
	if err := m.Rename(id, "/other", "/wp-content"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename over an existing name: %v", err)
	}
	for _, bad := range []string{"4755", "000", "1777", "9", "abc", "0644x"} {
		if err := m.Chmod(id, "/wp-content/hello.txt", bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("chmod %s accepted", bad)
		}
	}
	if err := m.Chmod(id, "/wp-content", "0755"); err != nil {
		t.Fatal(err)
	}
	// no leftover temp files
	l, _ = m.ListFiles(id, "/wp-content")
	if names(l) != "file:hello.txt" {
		t.Fatalf("left over: %s", names(l))
	}
}

func TestFilesNeverLeaveTheSiteFolder(t *testing.T) {
	m, _, id := readySite(t)
	wp := m.wpDir(id)
	// what a hacked WordPress could plant: links to the secret, the site config and the host
	for name, target := range map[string]string{"pw": "../secrets/db_password", "cfg": "../site.json", "host": "/etc/passwd", "up": ".."} {
		if err := os.Symlink(target, filepath.Join(wp, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/pw", "/cfg", "/host", "/up/site.json", "/../secrets/db_password"} {
		if f, _, err := m.OpenFile(id, p); err == nil {
			f.Close()
			t.Errorf("%s readable outside the site folder", p)
		}
		if _, err := m.WriteFile(id, p, strings.NewReader("x"), true); err == nil {
			t.Errorf("%s writable outside the site folder", p)
		}
		if _, err := m.ListFiles(id, p); err == nil {
			t.Errorf("%s listable", p)
		}
	}
	// ".." above the site folder stays inside it
	cfg, _ := os.ReadFile(filepath.Join(m.dir(id), "site.json"))
	if _, err := m.WriteFile(id, "/../site.json", strings.NewReader("{}"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.dir(id), "site.json")); !bytes.Equal(b, cfg) {
		t.Fatal("/../site.json reached the real site.json")
	}
	_, _ = m.DeleteFile(id, "/site.json")
	for _, p := range []string{"/pw", "/host", "/up/site.json"} {
		if _, _, err := m.OpenFile(id, p); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v, want a bad-input answer (400), not a server error", p, err)
		}
	}
	if err := m.Chmod(id, "/host", "777"); err == nil {
		t.Error("chmod through a link to the host")
	}
	if _, err := m.WriteFile(id, "/up/x.txt", strings.NewReader("x"), false); err == nil {
		t.Error("write through a link to the parent")
	}
	sec, _ := os.ReadFile(filepath.Join(m.dir(id), "secrets", "db_password"))
	// deleting a link removes the link only
	if _, err := m.DeleteFile(id, "/pw"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(m.dir(id), "secrets", "db_password")); err != nil || !bytes.Equal(b, sec) {
		t.Fatal("deleting a link touched its target")
	}
	var buf bytes.Buffer
	if err := m.ZipFolder(id, "/", &buf); err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	for _, f := range zr.File {
		if f.Name != "" {
			t.Errorf("zip of / holds %s (links must be skipped)", f.Name)
		}
	}
	if _, err := m.DeleteFile(id, "/"); !errors.Is(err, ErrBadInput) {
		t.Fatal("the site folder itself deleted")
	}
	if _, err := m.ListFiles("../etc", "/"); !errors.Is(err, ErrNotFound) {
		t.Fatal("bad site id accepted")
	}
}

func TestFilesTrashRestoreAndPurge(t *testing.T) {
	m, _, id := readySite(t)
	_ = m.Mkdir(id, "/themes")
	_, _ = m.WriteFile(id, "/themes/style.css", strings.NewReader("body{}"), false)
	tr, err := m.DeleteFile(id, "/themes")
	if err != nil || tr.Path != "/themes" || tr.Type != "dir" {
		t.Fatalf("delete: %+v %v", tr, err)
	}
	if _, err := m.ListFiles(id, "/themes"); err == nil {
		t.Fatal("folder still there")
	}
	list, _ := m.FilesTrash(id)
	if len(list) != 1 || list[0].Entry != tr.Entry {
		t.Fatalf("trash: %+v", list)
	}
	_ = m.Mkdir(id, "/themes")
	if _, err := m.RestoreFile(id, tr.Entry); !errors.Is(err, ErrExists) {
		t.Fatalf("restore over a new folder: %v", err)
	}
	_, _ = m.DeleteFile(id, "/themes") // the new empty one
	if p, err := m.RestoreFile(id, tr.Entry); err != nil || p != "/themes" {
		t.Fatalf("restore: %s %v", p, err)
	}
	if f, _, err := m.OpenFile(id, "/themes/style.css"); err != nil {
		t.Fatal("restored file missing")
	} else {
		f.Close()
	}
	if _, err := m.RestoreFile(id, "../../x"); !errors.Is(err, ErrBadInput) {
		t.Fatal("bad trash entry accepted")
	}
	// one entry left (the empty folder); it goes after TrashDays
	m.now = func() time.Time { return time.Now().Add((TrashDays + 1) * 24 * time.Hour) }
	if _, err := m.Purge(); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.FilesTrash(id); len(list) != 0 {
		t.Fatalf("purge left %d entries", len(list))
	}
}

func mkzip(t *testing.T, files map[string]string, link string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte(c))
	}
	if link != "" {
		h := &zip.FileHeader{Name: link}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write([]byte("/etc/passwd"))
	}
	zw.Close()
	return b.Bytes()
}

func TestUnzipStaysInsideAndZipRoundTrips(t *testing.T) {
	m, _, id := readySite(t)
	_ = m.Mkdir(id, "/plugins")
	good := mkzip(t, map[string]string{"my-plugin/my-plugin.php": "<?php", "my-plugin/readme..txt": "two dots are fine"}, "")
	if _, err := m.WriteFile(id, "/plugins/p.zip", bytes.NewReader(good), false); err != nil {
		t.Fatal(err)
	}
	n, err := m.Unzip(id, "/plugins/p.zip")
	if err != nil || n != 2 {
		t.Fatalf("unzip: %d %v", n, err)
	}
	l, _ := m.ListFiles(id, "/plugins/my-plugin")
	if names(l) != "file:my-plugin.php file:readme..txt" {
		t.Fatalf("unzipped: %s", names(l))
	}
	for name, z := range map[string][]byte{
		"slip":  mkzip(t, map[string]string{"../../site.json": "{}"}, ""),
		"abs":   mkzip(t, map[string]string{"/etc/cron.d/x": "x"}, ""),
		"link":  mkzip(t, nil, "evil"),
		"plain": []byte("not a zip"),
	} {
		_, _ = m.WriteFile(id, "/plugins/"+name+".zip", bytes.NewReader(z), false)
		if _, err := m.Unzip(id, "/plugins/"+name+".zip"); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s zip: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(m.dir(id), "site.json")); string(b) == "{}" {
		t.Fatal("zip slip overwrote site.json")
	}
	if _, err := m.Unzip(id, "/plugins/my-plugin/my-plugin.php"); !errors.Is(err, ErrBadInput) {
		t.Fatal("non-zip extracted")
	}
	var buf bytes.Buffer
	if err := m.ZipFolder(id, "/plugins/my-plugin", &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range zr.File {
		got = append(got, f.Name)
	}
	if strings.Join(got, " ") != "my-plugin/ my-plugin/my-plugin.php my-plugin/readme..txt" {
		t.Fatalf("zip names: %v", got)
	}
}
