package sites

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ticking makes every call of m.now one second later, so backup names never collide.
func ticking(m *Manager, start time.Time) {
	var mu sync.Mutex
	t := start
	m.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}

func entries(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	first := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = h.Name
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
		if h.Typeflag == tar.TypeSymlink {
			got[h.Name] = "->" + h.Linkname
		}
	}
	if first != "manifest.json" {
		t.Fatalf("first entry %q", first)
	}
	return got
}

func TestBackupAndRestore(t *testing.T) {
	m, f, id := readySite(t)
	ticking(m, time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC))
	wp := m.wpDir(id)
	_ = os.MkdirAll(filepath.Join(wp, "wp-content/uploads"), 0o755)
	_ = os.WriteFile(filepath.Join(wp, "index.php"), []byte("<?php // v1"), 0o644)
	_ = os.WriteFile(filepath.Join(wp, "wp-content/uploads/a.jpg"), []byte("JPEG"), 0o640)
	_ = os.Symlink("/etc/passwd", filepath.Join(wp, "link"))
	_ = os.WriteFile(filepath.Join(wp, ".np-upload-x"), []byte("partial"), 0o600)
	f.dumped = "-- MariaDB dump\nCREATE TABLE wp_posts (id int);\n"

	job, err := m.StartBackup(id)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("backup: %+v", v)
	}
	b, err := m.ListBackups(id)
	if err != nil || len(b.Backups) != 1 || b.Backups[0].Kind != "manual" || !strings.HasPrefix(b.Backups[0].Name, "manual-20261112-15") || b.Keep != KeepDefault || b.Daily {
		t.Fatalf("list: %+v %v", b, err)
	}
	name := b.Backups[0].Name
	p := filepath.Join(m.backupDir(id), name)
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", fi.Mode())
	}
	got := entries(t, p)
	var man Manifest
	if err := json.Unmarshal([]byte(got["manifest.json"]), &man); err != nil || man.Format != BackupFormat || man.Site != id || !man.Complete || man.DBBytes != int64(len(f.dumped)) {
		t.Fatalf("manifest %+v %v", man, err)
	}
	if got["database.sql"] != f.dumped || got["wp/index.php"] != "<?php // v1" || got["wp/wp-content/uploads/a.jpg"] != "JPEG" || got["wp/link"] != "->/etc/passwd" {
		t.Fatalf("archive: %v", got)
	}
	if _, ok := got["wp/.np-upload-x"]; ok {
		t.Fatal("unfinished upload archived")
	}
	dump := false
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "mariadb-dump") && strings.Contains(j, "--single-transaction") {
			dump = true
		}
	}
	if !dump {
		t.Fatal("no consistent dump")
	}
	ents, _ := os.ReadDir(m.backupDir(id))
	for _, e := range ents {
		if e.Name() != name {
			t.Fatalf("left over in backups: %s", e.Name())
		}
	}

	// change the site, then restore everything
	_ = os.WriteFile(filepath.Join(wp, "index.php"), []byte("<?php // hacked"), 0o644)
	_ = os.WriteFile(filepath.Join(wp, "evil.php"), []byte("x"), 0o644)
	n := len(f.stdin)
	job, err = m.StartRestore(id, name, "all")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("restore: %+v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "index.php")); string(b) != "<?php // v1" {
		t.Fatalf("index.php = %q", b)
	}
	if _, err := os.Stat(filepath.Join(wp, "evil.php")); err == nil {
		t.Fatal("file made after the backup survived a restore")
	}
	if l, _ := os.Readlink(filepath.Join(wp, "link")); l != "/etc/passwd" {
		t.Fatalf("link %q", l)
	}
	if fi, _ := os.Stat(filepath.Join(wp, "wp-content/uploads/a.jpg")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	in := f.stdin[n:]
	if len(in) != 2 || in[0] != resetSQL || in[1] != f.dumped {
		t.Fatalf("database fed %q", in)
	}
	var stop, up bool
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		stop = stop || strings.HasSuffix(j, " stop web")
		up = up || strings.Contains(j, "up -d --force-recreate --wait")
	}
	if !stop || !up {
		t.Fatal("web not stopped and recreated")
	}
	b, _ = m.ListBackups(id)
	if len(b.Backups) != 2 || b.Backups[0].Kind != "before-restore" {
		t.Fatalf("no safety copy: %+v", b.Backups)
	}
	if got := entries(t, filepath.Join(m.backupDir(id), b.Backups[0].Name)); got["wp/evil.php"] != "x" {
		t.Fatal("safety copy does not hold the state before restore")
	}
	sd, _ := os.ReadDir(m.dir(id))
	for _, e := range sd {
		if strings.HasPrefix(e.Name(), "wp.") {
			t.Fatalf("left over: %s", e.Name())
		}
	}

	// database only: files stay as they are
	_ = os.WriteFile(filepath.Join(wp, "new.txt"), []byte("keep"), 0o644)
	n = len(f.stdin)
	job, _ = m.StartRestore(id, name, "database")
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("db restore: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(wp, "new.txt")); err != nil {
		t.Fatal("database restore touched files")
	}
	if len(f.stdin)-n != 2 {
		t.Fatal("database not restored")
	}
	// files only: the database is not touched
	n = len(f.stdin)
	job, _ = m.StartRestore(id, name, "files")
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("files restore: %+v", v)
	}
	if len(f.stdin) != n {
		t.Fatal("files restore fed the database")
	}
	if _, err := os.Stat(filepath.Join(wp, "new.txt")); err == nil {
		t.Fatal("files restore kept a newer file")
	}

	// when the safety copy fails nothing is changed
	_ = os.WriteFile(filepath.Join(wp, "index.php"), []byte("<?php // v2"), 0o644)
	f.fail = "mariadb-dump"
	job, _ = m.StartRestore(id, name, "all")
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "nothing was changed") {
		t.Fatalf("restore without safety copy: %+v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "index.php")); string(b) != "<?php // v2" {
		t.Fatal("restore changed files without a safety copy")
	}
	f.fail = ""

	// bad input
	for _, bad := range []string{"../site.json", "manual-x.tar.gz", "site.json", "manual-20261112-150000.tar.gz.part"} {
		if _, _, err := m.OpenBackup(id, bad); err == nil {
			t.Errorf("backup %s served", bad)
		}
		if _, err := m.StartRestore(id, bad, "all"); err == nil {
			t.Errorf("restore of %s started", bad)
		}
	}
	if _, err := m.StartRestore(id, name, "everything"); !errors.Is(err, ErrBadInput) {
		t.Fatal("unknown restore part accepted")
	}
	if _, err := m.StartBackup("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown site")
	}
	if fh, _, err := m.OpenBackup(id, name); err != nil {
		t.Fatal(err)
	} else {
		fh.Close()
	}
	if err := m.DeleteBackup(id, name); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteBackup(id, name); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted twice")
	}
}

// craft writes a backup archive by hand (for hostile archives).
func craft(t *testing.T, p string, man Manifest, ents map[string]string) {
	t.Helper()
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	mb, _ := json.Marshal(man)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(mb)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(mb)
	_ = tw.WriteHeader(&tar.Header{Name: "database.sql", Mode: 0o600, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("--"))
	for n, body := range ents {
		h := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if strings.HasPrefix(body, "->") {
			h = &tar.Header{Name: n, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: body[2:]}
		}
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = f.Close()
}

func TestRestoreRefusesHostileArchives(t *testing.T) {
	m, f, id := readySite(t)
	ticking(m, time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC))
	f.dumped = "--"
	wp := m.wpDir(id)
	_ = os.WriteFile(filepath.Join(wp, "index.php"), []byte("ok"), 0o644)
	_ = os.MkdirAll(m.backupDir(id), 0o700)
	good := Manifest{Format: BackupFormat, Site: id, Complete: true}
	outside := filepath.Join(t.TempDir(), "pwned")
	cases := map[string]struct {
		man  Manifest
		ents map[string]string
		want string
	}{
		"manual-20260101-000001.tar.gz": {good, map[string]string{"wp/../../escape.php": "x"}, "outside the website"},
		"manual-20260101-000002.tar.gz": {good, map[string]string{"/etc/cron.d/x": "x"}, "outside the website"},
		"manual-20260101-000003.tar.gz": {good, map[string]string{"wp/l": "->" + filepath.Dir(outside), "wp/l/pwned": "x"}, "file restore failed"},
		"manual-20260101-000004.tar.gz": {Manifest{Format: "other/1", Site: id, Complete: true}, nil, "format"},
		"manual-20260101-000005.tar.gz": {Manifest{Format: BackupFormat, Site: "other", Complete: true}, nil, "belongs to website"},
		"manual-20260101-000006.tar.gz": {Manifest{Format: BackupFormat, Site: id}, nil, "format"},
	}
	for name, c := range cases {
		craft(t, filepath.Join(m.backupDir(id), name), c.man, c.ents)
		job, err := m.StartRestore(id, name, "files")
		if err == nil {
			v := waitJob(t, m, job)
			if v.State != "failed" {
				t.Errorf("%s: restored", name)
			}
			err = errors.New(v.Error)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("archive wrote outside the website")
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "index.php")); string(b) != "ok" {
		t.Fatal("failed restore changed the website")
	}
	sd, _ := os.ReadDir(m.dir(id))
	for _, e := range sd {
		if strings.HasPrefix(e.Name(), "wp.") {
			t.Fatalf("left over: %s", e.Name())
		}
	}
}

func TestDailyBackupsAndKeep(t *testing.T) {
	m, f, id := readySite(t)
	f.dumped = "--"
	if err := m.SetSchedule(id, true, 0); !errors.Is(err, ErrBadInput) {
		t.Fatal("keep 0 accepted")
	}
	if err := m.SetSchedule(id, true, KeepMax+1); !errors.Is(err, ErrBadInput) {
		t.Fatal("keep above max accepted")
	}
	if err := m.SetSchedule(id, true, 2); err != nil {
		t.Fatal(err)
	}
	if b, _ := m.ListBackups(id); !b.Daily || b.Keep != 2 {
		t.Fatalf("schedule not saved: %+v", b)
	}
	// before 02:00 WIB nothing runs; after, one backup a day
	day := time.Date(2026, 11, 12, 18, 30, 0, 0, time.UTC) // 01:30 WIB on the 13th
	m.now = func() time.Time { return day }
	if e := m.RunDailyBackups(context.Background()); len(e) != 0 {
		t.Fatal(e)
	}
	if b, _ := m.ListBackups(id); len(b.Backups) != 0 {
		t.Fatal("backup before the daily hour")
	}
	for d := 0; d < 4; d++ {
		at := day.Add(time.Duration(d)*24*time.Hour + time.Hour)
		m.now = func() time.Time { return at }
		for i := 0; i < 2; i++ {
			if e := m.RunDailyBackups(context.Background()); len(e) != 0 {
				t.Fatal(e)
			}
		}
		if i := len(m.jobs.List()); i == 0 {
			t.Fatal("no job")
		}
	}
	b, _ := m.ListBackups(id)
	if len(b.Backups) != 2 || b.Backups[0].Name != "daily-20261116-023000.tar.gz" || b.Backups[1].Name != "daily-20261115-023000.tar.gz" {
		t.Fatalf("daily backups kept: %+v", b.Backups)
	}
	// a manual backup is never pruned by the daily keep
	ticking(m, day.Add(5*24*time.Hour))
	job, _ := m.StartBackup(id)
	waitJob(t, m, job)
	if err := m.SetSchedule(id, false, 1); err != nil {
		t.Fatal(err)
	}
	b, _ = m.ListBackups(id)
	if len(b.Backups) != 2 || b.Backups[0].Kind != "manual" || b.Backups[1].Name != "daily-20261116-023000.tar.gz" || b.Daily {
		t.Fatalf("after keep 1: %+v", b.Backups)
	}
	at := day.Add(6*24*time.Hour + time.Hour)
	m.now = func() time.Time { return at }
	m.RunDailyBackups(context.Background())
	if b2, _ := m.ListBackups(id); len(b2.Backups) != 2 {
		t.Fatal("daily backup ran while switched off")
	}
	// a failed daily backup is reported and leaves nothing behind
	_ = m.SetSchedule(id, true, 1)
	f.fail = "mariadb-dump"
	if e := m.RunDailyBackups(context.Background()); len(e) != 1 || !strings.Contains(e[0], "database copy failed") {
		t.Fatalf("errors %v", e)
	}
	ents, _ := os.ReadDir(m.backupDir(id))
	for _, e := range ents {
		if !backupRe.MatchString(e.Name()) {
			t.Fatalf("left over: %s", e.Name())
		}
	}
}
