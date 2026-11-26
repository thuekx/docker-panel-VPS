package sites

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/npbk"
)

const serverPass = "a long server passphrase"

// serverSite is a ready WordPress website with files, an SFTP account and a protected folder, plus the panel's copy.
func serverSite(t *testing.T) (*Manager, *fakeRunner, string) {
	t.Helper()
	m, f, id := readySite(t)
	ticking(m, time.Now())
	m.cfg.PanelData, m.cfg.PanelUID = t.TempDir(), os.Getuid()
	wp := m.wpDir(id)
	_ = os.MkdirAll(filepath.Join(wp, "wp-content"), 0o755)
	_ = os.WriteFile(filepath.Join(wp, "index.php"), []byte("<?php // live"), 0o644)
	f.dumped = "-- MariaDB dump\nCREATE TABLE wp_posts (id int);\n"
	b, _ := json.Marshal(sftpFile{Site: id, Users: []SFTPUser{{Name: "deployer", Hash: "$2a$10$x"}}})
	_ = os.MkdirAll(m.sftpDir(), 0o700)
	_ = os.WriteFile(m.sftpFileOf(id), b, 0o600)
	s, _ := m.Get(id)
	s.Protected = []Protected{{Path: "/admin", Users: []string{"boss"}}}
	_ = writeSite(m.dir(id), s)
	_ = os.MkdirAll(filepath.Dir(m.authFile(id, "/admin")), 0o755)
	_ = os.WriteFile(m.authFile(id, "/admin"), []byte("boss:$apr1$hash\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(m.dir(id), "backups"), 0o700)
	_ = os.WriteFile(filepath.Join(m.dir(id), "backups", "daily-20261101-020000.tar.gz"), []byte("old"), 0o600)
	writePanelCopy(t, m)
	return m, f, id
}

func writePanelCopy(t *testing.T, m *Manager) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(m.panelDir(), PanelCopy), []byte("SQLite format 3\x00 panel"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(m.panelDir(), "alerts.key"), []byte("0123456789abcdef0123456789abcdef"), 0o600)
}

func makeServerBackup(t *testing.T, m *Manager) string {
	t.Helper()
	job, err := m.StartServerBackup(serverPass, "v1.34.0", "v1.34.0")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("server backup: %+v", v)
	}
	b, err := m.ListServerBackups()
	if err != nil || len(b.Backups) == 0 {
		t.Fatalf("list %+v %v", b, err)
	}
	return filepath.Join(m.serverBackupDir(), b.Backups[0].Name)
}

func TestServerBackupAndRestore(t *testing.T) {
	m, _, id := serverSite(t)
	if _, err := m.StartServerBackup("too short", "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("short passphrase: %v", err)
	}
	file := makeServerBackup(t, m)
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(m.serverBackupDir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(m.panelDir(), PanelCopy)); err == nil {
		t.Fatal("the panel's copy was left behind")
	}
	if _, err := m.StartServerBackup(serverPass, "", ""); !errors.Is(err, ErrBadInput) {
		t.Fatalf("no panel copy: %v", err)
	}
	if b, _ := m.ListBackups(id); len(b.Backups) != 1 {
		t.Fatalf("the website's backup list changed: %+v", b.Backups)
	}
	raw, _ := os.ReadFile(file)
	for _, secret := range []string{serverPass, "<?php // live", "boss:$apr1$hash", "SQLite format 3"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%q readable in the file", secret)
		}
	}
	if _, err := CheckServerBackup(file, "a wrong passphrase!!"); !errors.Is(err, npbk.ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	man, err := CheckServerBackup(file, serverPass)
	if err != nil || len(man.Sites) != 1 || man.Sites[0].ID != id || man.Sites[0].Engine != "docker" || !man.AlertKey || man.Panel != "v1.34.0" {
		t.Fatalf("manifest %+v %v", man, err)
	}

	// a new server
	n, f2 := newTest(t, nil)
	n.cfg.PanelData, n.cfg.PanelUID, n.cfg.FileUID = t.TempDir(), os.Getuid(), os.Getuid()
	if err := n.RestorePanel(file, "a wrong passphrase!!"); !errors.Is(err, npbk.ErrPassphrase) {
		t.Fatalf("panel, wrong passphrase: %v", err)
	}
	_ = os.WriteFile(filepath.Join(n.panelDir(), "panel.db"), []byte("fresh"), 0o600)
	_ = os.WriteFile(filepath.Join(n.panelDir(), "panel.db-wal"), []byte("wal"), 0o600)
	if err := n.RestorePanel(file, serverPass); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(n.panelDir(), "panel.db")); string(b) != "SQLite format 3\x00 panel" {
		t.Fatalf("panel.db %q", b)
	}
	if _, err := os.Stat(filepath.Join(n.panelDir(), "panel.db-wal")); err == nil {
		t.Fatal("old WAL file kept")
	}
	if b, _ := os.ReadFile(filepath.Join(n.panelDir(), "alerts.key")); len(b) != 32 {
		t.Fatalf("alerts.key %q", b)
	}
	var lines []string
	logf := func(f string, a ...any) { lines = append(lines, f) }
	res, err := n.RestoreSites(context.Background(), file, serverPass, logf)
	if err != nil || len(res) != 1 || res[0].Result != "restored" {
		t.Fatalf("restore %+v %v", res, err)
	}
	for _, p := range []string{"site.json", "compose.yml", "secrets/db_password"} {
		a, _ := os.ReadFile(filepath.Join(m.dir(id), p))
		b, err := os.ReadFile(filepath.Join(n.dir(id), p))
		if err != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s not restored: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(n.wpDir(id), "index.php")); string(b) != "<?php // live" {
		t.Fatalf("files not restored: %q", b)
	}
	if b, _ := os.ReadFile(n.authFile(id, "/admin")); string(b) != "boss:$apr1$hash\n" {
		t.Fatalf("password file %q", b)
	}
	if b, _ := os.ReadFile(n.sftpFileOf(id)); !strings.Contains(string(b), "deployer") {
		t.Fatalf("sftp accounts %q", b)
	}
	if _, err := os.Stat(filepath.Join(n.dir(id), "backups", "daily-20261101-020000.tar.gz")); err == nil {
		t.Fatal("old backups carried over")
	}
	calls := ""
	for _, c := range f2.calls {
		calls += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(calls, "up -d --wait --wait-timeout 300 db") || !strings.Contains(strings.Join(f2.stdin, "\n"), "CREATE TABLE wp_posts") {
		t.Fatalf("database not loaded:\n%s", calls)
	}
	if _, err := os.Stat(filepath.Join(n.cfg.Data, "server-restore")); err == nil {
		t.Fatal("work folder left behind")
	}
	// a second run leaves the website alone
	res, err = n.RestoreSites(context.Background(), file, serverPass, logf)
	if err != nil || len(res) != 1 || res[0].Result != "skipped" {
		t.Fatalf("second restore %+v %v", res, err)
	}
}

func TestServerBackupKeepsThree(t *testing.T) {
	m, _, _ := serverSite(t)
	for i := 0; i < 4; i++ {
		writePanelCopy(t, m)
		makeServerBackup(t, m)
	}
	b, _ := m.ListServerBackups()
	if len(b.Backups) != ServerBackupKeep {
		t.Fatalf("kept %d", len(b.Backups))
	}
	if err := m.DeleteServerBackup("../x.npbk"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad name: %v", err)
	}
	if err := m.DeleteServerBackup(b.Backups[0].Name); err != nil {
		t.Fatal(err)
	}
}

func TestServerBackupFailedWebsiteLeavesNothing(t *testing.T) {
	m, f, _ := serverSite(t)
	f.fail = "mariadb-dump"
	job, err := m.StartServerBackup(serverPass, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" {
		t.Fatalf("job %+v", v)
	}
	ents, _ := os.ReadDir(m.serverBackupDir())
	if len(ents) != 0 {
		t.Fatalf("left over: %v", ents)
	}
}

// sealed writes a server backup by hand.
func sealed(t *testing.T, entries map[string]string, order []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.npbk")
	f, _ := os.Create(p)
	w, err := npbk.NewWriter(f, serverPass)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(w)
	for _, n := range order {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: int64(len(entries[n])), Typeflag: tar.TypeReg})
		tw.Write([]byte(entries[n]))
	}
	tw.Close()
	w.Close()
	f.Close()
	return p
}

func TestCheckServerBackupRefuses(t *testing.T) {
	man := func(m ServerManifest) string { b, _ := json.Marshal(m); return string(b) }
	ok := ServerManifest{Format: ServerBackupFormat, Complete: true}
	withSite := ok
	withSite.Sites = []ServerSite{{ID: "voltix-id"}}
	cases := map[string]struct {
		e     map[string]string
		order []string
	}{
		"no manifest":        {map[string]string{"panel/panel.db": "x"}, []string{"panel/panel.db"}},
		"not complete":       {map[string]string{"panel/panel.db": "x", "manifest.json": man(ServerManifest{Format: ServerBackupFormat})}, []string{"panel/panel.db", "manifest.json"}},
		"manifest not last":  {map[string]string{"panel/panel.db": "x", "manifest.json": man(ok), "sites/voltix-id/site.tar": ""}, []string{"panel/panel.db", "manifest.json", "sites/voltix-id/site.tar"}},
		"no panel":           {map[string]string{"manifest.json": man(ok)}, []string{"manifest.json"}},
		"path outside":       {map[string]string{"../etc/passwd": "x", "panel/panel.db": "x", "manifest.json": man(ok)}, []string{"../etc/passwd", "panel/panel.db", "manifest.json"}},
		"website incomplete": {map[string]string{"panel/panel.db": "x", "sites/voltix-id/site.tar": "", "manifest.json": man(withSite)}, []string{"panel/panel.db", "sites/voltix-id/site.tar", "manifest.json"}},
		"website unlisted":   {map[string]string{"panel/panel.db": "x", "sites/other/site.tar": "", "manifest.json": man(ok)}, []string{"panel/panel.db", "sites/other/site.tar", "manifest.json"}},
	}
	for name, c := range cases {
		if _, err := CheckServerBackup(sealed(t, c.e, c.order), serverPass); !errors.Is(err, npbk.ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := sealed(t, map[string]string{"panel/panel.db": "x", "manifest.json": man(ok)}, []string{"panel/panel.db", "manifest.json"})
	if _, err := CheckServerBackup(good, serverPass); err != nil {
		t.Fatalf("good file: %v", err)
	}
}
