package sites

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "k3y-k3y-k3y-k3y-k3y-k3y-k3y-k3y-k3y-k3y-k3y" // 43 characters, like the plugin's

func mkCode(home, key string) string {
	b, _ := json.Marshal(map[string]string{"u": home, "k": key})
	return "nm1." + base64.RawURLEncoding.EncodeToString(b)
}

func TestParseMigrateCode(t *testing.T) {
	c, err := ParseMigrateCode("  " + mkCode("https://old.example.com/blog", testKey) + "\n")
	if err != nil || c.Home.Host != "old.example.com" || c.Home.Path != "/blog" || c.Key != testKey {
		t.Fatalf("good code: %+v %v", c, err)
	}
	for name, code := range map[string]string{
		"http":       mkCode("http://old.example.com", testKey),
		"short key":  mkCode("https://old.example.com", "abc"),
		"odd key":    mkCode("https://old.example.com", strings.Repeat("a", 40)+"$%"),
		"user":       mkCode("https://me:pw@old.example.com", testKey),
		"query":      mkCode("https://old.example.com/?x=1", testKey),
		"no prefix":  strings.TrimPrefix(mkCode("https://old.example.com", testKey), "nm1."),
		"not base64": "nm1.@@@",
		"not json":   "nm1." + base64.RawURLEncoding.EncodeToString([]byte("hello")),
		"empty":      "",
	} {
		if _, err := ParseMigrateCode(code); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := ParseMigrateCode(mkCode("http://old.example.com", testKey)); !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("http code should say HTTPS: %v", err)
	}
}

func TestPublicAddr(t *testing.T) {
	m, _ := newTest(t, nil)
	for a, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.17.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "::": false, "fd00::1": false, "fe80::1": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false,
		ip: false, // this server itself
	} {
		if got := m.publicAddr(netip.MustParseAddr(a)); got != want {
			t.Errorf("%s: %v, want %v", a, got, want)
		}
	}
	m.cfg.MigrateTestAllow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	if !m.publicAddr(netip.MustParseAddr("127.0.0.1")) || m.publicAddr(netip.MustParseAddr("127.0.0.2")) {
		t.Error("test allow-list")
	}
}

// tlsOn starts a TLS test server on one loopback address and writes its certificate for the agent to trust.
func tlsOn(t *testing.T, addr string, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	l, err := net.Listen("tcp", addr+":0")
	if err != nil {
		t.Skip("no loopback address", addr, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = l
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca-"+addr+".pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	return srv, ca
}

func TestMigrateClientGuard(t *testing.T) {
	m, _ := newTest(t, nil)
	inner, _ := tlsOn(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secret") }))
	outer, ca := tlsOn(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, inner.URL+"/", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
		default:
			io.WriteString(w, "ok")
		}
	}))
	// the test servers present one certificate (httptest's), so one CA file trusts both
	m.cfg.MigrateTestCA = ca
	get := func(u string) error {
		c, err := m.migrateClient()
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Get(u)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(outer.URL + "/"); !errors.Is(err, errBlockedAddr) {
		t.Fatalf("loopback without the allow-list: %v", err)
	}
	m.cfg.MigrateTestAllow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	if err := get(outer.URL + "/"); err != nil {
		t.Fatalf("allowed address: %v", err)
	}
	if err := get(outer.URL + "/to-private"); !errors.Is(err, errBlockedAddr) {
		t.Fatalf("redirect to a private address: %v", err)
	}
	if err := get(outer.URL + "/to-http"); err == nil || !strings.Contains(err.Error(), "away from https") {
		t.Fatalf("redirect to http: %v", err)
	}
	if err := get(strings.Replace(outer.URL, "https:", "http:", 1)); err == nil {
		t.Fatal("plain http answered")
	}
}

func TestSQLFilter(t *testing.T) {
	in := "CREATE DATABASE IF NOT EXISTS `old_db` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n" +
		"USE `old_db`;\n" +
		"CREATE TABLE `wp_users` (id int);\n" +
		"/*!50013 DEFINER=`olduser`@`localhost` SQL SECURITY DEFINER */\n" +
		"CREATE DEFINER='x'@'%' TRIGGER t BEFORE INSERT ON wp_users FOR EACH ROW SET @a=1;\n" +
		"INSERT INTO `wp_posts` VALUES (1,'USE the force; CREATE DATABASE is text here');\n" +
		"last line without newline"
	b, err := io.ReadAll(&sqlFilter{br: bufio.NewReader(strings.NewReader(in))})
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, gone := range []string{"CREATE DATABASE IF", "USE `old_db`", "olduser", "'x'@'%'"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q kept:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"CREATE TABLE `wp_users`", "CREATE  TRIGGER t", "USE the force; CREATE DATABASE is text here", "last line without newline"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q lost:\n%s", kept, out)
		}
	}
}

func TestReplaceCmds(t *testing.T) {
	cmds, err := replaceCmds("http://old.example.com/blog/", "https://voltix-id.preview.nostek.dev")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cmds {
		got = append(got, strings.Join(c, " "))
	}
	tail := " --all-tables-with-prefix --precise --skip-columns=guid --report-changed-only"
	want := []string{
		"wp search-replace //old.example.com/blog //voltix-id.preview.nostek.dev" + tail,
		`wp search-replace \/\/old.example.com\/blog \/\/voltix-id.preview.nostek.dev` + tail,
		"wp search-replace http://voltix-id.preview.nostek.dev https://voltix-id.preview.nostek.dev" + tail,
		`wp search-replace http:\/\/voltix-id.preview.nostek.dev https:\/\/voltix-id.preview.nostek.dev` + tail,
		"wp option update home https://voltix-id.preview.nostek.dev",
		"wp option update siteurl https://voltix-id.preview.nostek.dev",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	if _, err := replaceCmds("not a url", "https://x"); !errors.Is(err, ErrBadInput) {
		t.Fatal("bad old address accepted")
	}
}

// ---------- archives ----------

type ent struct {
	name, body, link string
	dir              bool
}

func tarOf(t *testing.T, ents []ent) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		switch {
		case e.dir:
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		case e.link != "":
			h = &tar.Header{Name: e.name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: e.link}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return b.Bytes()
}

func gz(b []byte) []byte {
	var o bytes.Buffer
	w := gzip.NewWriter(&o)
	_, _ = w.Write(b)
	_ = w.Close()
	return o.Bytes()
}

func wpTree(dir, db, prefix string) []ent {
	p := dir + "/"
	return []ent{
		{name: p, dir: true},
		{name: p + "wp-includes/version.php", body: "<?php\n$wp_version = '6.8.3';\n"},
		{name: p + "wp-config.php", body: "<?php\ndefine( 'DB_NAME', '" + db + "' );\ndefine('DB_PASSWORD', 'old-secret');\n$table_prefix = '" + prefix + "';\nrequire_once ABSPATH . 'wp-settings.php';\n"},
		{name: p + "index.php", body: "<?php // " + dir},
		{name: p + "wp-content/uploads/2026/11/a.jpg", body: "JPEG"},
		{name: p + "wp-content/cache/page.html", body: "cached"},
		{name: p + "wp-content/advanced-cache.php", body: "<?php // old cache"},
		{name: p + "wp-content/themes/t/link", link: "../../uploads"},
	}
}

const oldDump = "-- MySQL dump\nCREATE DATABASE `cpuser_wp`;\nUSE `cpuser_wp`;\nCREATE TABLE `np_options` (id int);\nINSERT INTO `np_options` VALUES (1);\n"

// cpanelFull is a cPanel full backup: homedir.tar nested inside, two WordPress sites, two databases.
func cpanelFull(t *testing.T) []byte {
	home := tarOf(t, append(append(wpTree("public_html", "cpuser_wp", "np_"), wpTree("addon.example.com", "cpuser_addon", "wp_")...),
		wpTree("public_html/wp-content/updraft/oldcopy", "x", "wp_")...))
	return gz(tarOf(t, []ent{
		{name: "backup-11.16.2026_10-00-00_cpuser/", dir: true},
		{name: "backup-11.16.2026_10-00-00_cpuser/homedir.tar", body: string(home)},
		{name: "backup-11.16.2026_10-00-00_cpuser/mysql/cpuser_wp.sql", body: oldDump},
		{name: "backup-11.16.2026_10-00-00_cpuser/mysql/cpuser_addon.sql", body: "-- addon\n"},
		{name: "backup-11.16.2026_10-00-00_cpuser/mysql.sql", body: "-- grants\n"},
	}))
}

func TestInspectCPanelFullBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(p, cpanelFull(t), 0o600)
	found, dumps, err := inspectArchive(p)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for k := range found {
		dirs = append(dirs, k)
	}
	sort.Strings(dirs)
	top := "backup-11.16.2026_10-00-00_cpuser/homedir/"
	if strings.Join(dirs, " ") != top+"addon.example.com "+top+"public_html" {
		t.Fatalf("candidates %v (the copy in a backup plugin folder must be left out)", dirs)
	}
	c := found[top+"public_html"]
	if c.version != "6.8.3" {
		t.Errorf("version %q", c.version)
	}
	d, err := pickDump(dumps, c.config)
	if err != nil || d != "backup-11.16.2026_10-00-00_cpuser/mysql/cpuser_wp.sql" {
		t.Fatalf("dump %q %v (from %v)", d, err, dumps)
	}
	if _, err := pickDump(dumps, []byte("define('DB_NAME', 'nothing');")); err == nil {
		t.Fatal("no matching dump accepted")
	}
	if _, err := pickDump(nil, c.config); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "second file") {
		t.Fatalf("no dump: %v", err)
	}
}

func TestInspectRefusesEscapes(t *testing.T) {
	for name, ents := range map[string][]ent{
		"dotdot":   {{name: "../evil.php", body: "x"}},
		"absolute": {{name: "/etc/cron.d/x", body: "x"}},
		"inner":    {{name: "a/../../x", body: "x"}},
	} {
		p := filepath.Join(t.TempDir(), "a")
		_ = os.WriteFile(p, tarOf(t, ents), 0o600)
		if _, _, err := inspectArchive(p); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(p, []byte("this is not an archive at all, just some text that goes on"), 0o600)
	if _, _, err := inspectArchive(p); !errors.Is(err, ErrBadInput) {
		t.Errorf("text file: %v", err)
	}
}

func TestInspectPlainZip(t *testing.T) {
	files := map[string]string{}
	for _, e := range wpTree("site", "db1", "wp_") {
		if !e.dir && e.link == "" {
			files[e.name] = e.body
		}
	}
	files["site/db1.sql"] = "--"
	p := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(p, mkzip(t, files, ""), 0o600)
	found, dumps, err := inspectArchive(p)
	if err != nil || len(found) != 1 || found["site"] == nil || len(dumps) != 1 {
		t.Fatalf("%v %v %v", found, dumps, err)
	}
}

// ---------- the import core with the fake runner ----------

// fakeWPCLI answers WP-CLI batches: each command goes to answer, which gives its exit code and output.
func fakeWPCLI(f *fakeRunner, answer func(cmd string) (int, string)) *[]string {
	var mu sync.Mutex
	seen := &[]string{}
	f.hook = func(args []string) ([]byte, error, bool) {
		for i, a := range args {
			if a != "-c" || i+1 >= len(args) || !strings.Contains(args[i+1], "@@np-") {
				continue
			}
			script := args[i+1]
			mark := strings.Trim(strings.Fields(script[strings.Index(script, "@@np-"):])[0], `"`)
			var out strings.Builder
			n := 0
			for _, line := range strings.Split(script, "\n") {
				cmd, ok := strings.CutSuffix(line, " </dev/null >/tmp/np.o 2>/tmp/np.e; rc=$?")
				if !ok {
					continue
				}
				cmd = strings.ReplaceAll(strings.ReplaceAll(cmd, `'\''`, "'"), "'", "")
				mu.Lock()
				*seen = append(*seen, cmd)
				mu.Unlock()
				rc, o := answer(cmd)
				fmt.Fprintf(&out, "\n%s %d %d\n%s\n%s err\n", mark, n, rc, o, mark)
				n++
			}
			fmt.Fprintf(&out, "\n%s end\n", mark)
			return []byte(out.String()), nil, true
		}
		return nil, nil, false
	}
	return seen
}

// oldSiteWP plays WordPress after the move: home is the old address until search-replace and the update ran.
func oldSiteWP(oldHome string) func(string) (int, string) {
	var mu sync.Mutex
	home := oldHome
	return func(cmd string) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(cmd, "wp option get home"):
			return 0, home
		case strings.HasPrefix(cmd, "wp option update home "):
			home = strings.TrimPrefix(cmd, "wp option update home ")
			return 0, "Success"
		case strings.HasPrefix(cmd, "wp user list --role=administrator"):
			return 0, "1\n7"
		}
		return 0, "Success"
	}
}

func migrateSite(t *testing.T) (*Manager, *fakeRunner, Site) {
	t.Helper()
	m, f, id := readySite(t)
	ticking(m, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	f.dumped = "-- fresh site dump\n"
	_ = os.WriteFile(filepath.Join(m.wpDir(id), "wp-config.php"), []byte("<?php // the panel's own config"), 0o640)
	_ = os.WriteFile(filepath.Join(m.wpDir(id), "index.php"), []byte("<?php // fresh"), 0o644)
	s, _ := m.Get(id)
	return m, f, s
}

func TestMigrateFromCPanelBackup(t *testing.T) {
	m, f, s := migrateSite(t)
	seen := fakeWPCLI(f, oldSiteWP("https://cpuser.example.com"))
	if _, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("no upload yet: %v", err)
	}
	if n, err := m.MigrateUpload(s.ID, "archive", bytes.NewReader(cpanelFull(t))); err != nil || n == 0 {
		t.Fatal(n, err)
	}
	if _, err := m.MigrateUpload(s.ID, "../site.json", strings.NewReader("x")); !errors.Is(err, ErrBadInput) {
		t.Fatalf("odd part: %v", err)
	}
	if st, _ := m.MigrateStatus(s.ID); st.Archive == 0 || st.Database != 0 {
		t.Fatalf("status %+v", st)
	}

	// two sites in the backup: the job stops before any change and names both
	job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	v := waitJob(t, m, job)
	if v.State != "failed" || !strings.HasPrefix(v.Error, "choose: ") || !strings.Contains(v.Error, "homedir/public_html") || !strings.Contains(v.Error, "homedir/addon.example.com") {
		t.Fatalf("choose: %+v", v)
	}
	if b, _ := m.ListBackups(s.ID); len(b.Backups) != 0 {
		t.Fatal("a backup was made before the choice")
	}

	root := "backup-11.16.2026_10-00-00_cpuser/homedir/public_html"
	job, err = m.StartMigrate(s.ID, MigrateRequest{Way: "backup", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	v = waitJob(t, m, job)
	if v.State != "done" {
		t.Fatalf("migrate: %+v", v)
	}
	checkMoved(t, m, f, s, *seen, "https://cpuser.example.com", "public_html")
	if _, err := os.Stat(m.migrateDir(s.ID)); !os.IsNotExist(err) {
		t.Error("uploaded files kept after the job")
	}
}

func TestMigrateHomeBackupWithSeparateDatabase(t *testing.T) {
	m, f, s := migrateSite(t)
	seen := fakeWPCLI(f, oldSiteWP("http://www.old.example.com"))
	arc := gz(tarOf(t, wpTree("public_html", "cpuser_wp", "np_")))
	if _, err := m.MigrateUpload(s.ID, "archive", bytes.NewReader(arc)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MigrateUpload(s.ID, "database", bytes.NewReader(gz([]byte(oldDump)))); err != nil {
		t.Fatal(err)
	}
	job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("migrate: %+v", v)
	}
	checkMoved(t, m, f, s, *seen, "http://www.old.example.com", "public_html")
}

func TestMigrateRefusals(t *testing.T) {
	m, f, s := migrateSite(t)
	fakeWPCLI(f, oldSiteWP("https://old.example.com"))
	for name, ents := range map[string][]ent{
		"multisite": {{name: "wp-includes/version.php", body: "<?php $wp_version = '6.8';"}, {name: "wp-config.php", body: "<?php define('MULTISITE', true); $table_prefix = 'wp_';"}, {name: "a.sql", body: "--"}},
		"prefix":    {{name: "wp-includes/version.php", body: "<?php $wp_version = '6.8';"}, {name: "wp-config.php", body: "<?php $table_prefix = 'wp-x';"}, {name: "a.sql", body: "--"}},
		"no site":   {{name: "index.html", body: "hi"}},
		"no db":     {{name: "wp-includes/version.php", body: "<?php"}, {name: "wp-config.php", body: "<?php"}},
	} {
		if _, err := m.MigrateUpload(s.ID, "archive", bytes.NewReader(tarOf(t, ents))); err != nil {
			t.Fatal(err)
		}
		job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup"})
		if err != nil {
			t.Fatal(err)
		}
		if v := waitJob(t, m, job); v.State != "failed" {
			t.Errorf("%s: %+v", name, v)
		}
		_ = m.MigrateClear(s.ID)
	}
	if b, _ := m.ListBackups(s.ID); len(b.Backups) != 0 {
		t.Fatal("refused jobs made a backup")
	}
	for name, r := range map[string]MigrateRequest{
		"way":       {Way: "ssh"},
		"http link": {Way: "backup", Link: "http://old.example.com/b.tar.gz"},
		"user link": {Way: "backup", Link: "https://u:p@old.example.com/b.tar.gz"},
		"code":      {Way: "plugin", Code: "nm1.x"},
	} {
		if _, err := m.StartMigrate(s.ID, r); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMigrateDownloadLinkIsGuarded(t *testing.T) {
	m, f, s := migrateSite(t)
	fakeWPCLI(f, oldSiteWP("https://old.example.com"))
	srv, ca := tlsOn(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(gz(tarOf(t, wpTree("public_html", "cpuser_wp", "np_"))))
	}))
	m.cfg.MigrateTestCA = ca
	job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup", Link: srv.URL + "/backup.tar.gz?sig=abc"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "public internet address") {
		t.Fatalf("private link: %+v", v)
	}
}

// checkMoved checks what every migration leaves: the files, the kept config, the prefix, the database, the addresses.
func checkMoved(t *testing.T, m *Manager, f *fakeRunner, s Site, seen []string, oldHome, dir string) {
	t.Helper()
	wp := m.wpDir(s.ID)
	if b, _ := os.ReadFile(filepath.Join(wp, "index.php")); string(b) != "<?php // "+dir {
		t.Errorf("index.php = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "wp-content/uploads/2026/11/a.jpg")); string(b) != "JPEG" {
		t.Error("upload missing")
	}
	if l, _ := os.Readlink(filepath.Join(wp, "wp-content/themes/t/link")); l != "../../uploads" {
		t.Errorf("link = %q", l)
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "wp-config.php")); string(b) != "<?php // the panel's own config" {
		t.Errorf("wp-config.php = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(wp, "wp-config.old.php")); !strings.HasPrefix(string(b), oldConfigGuard) || !strings.Contains(string(b), "old-secret") {
		t.Errorf("wp-config.old.php = %q", b)
	}
	if _, err := os.Stat(filepath.Join(wp, "wp-content/cache")); err == nil {
		t.Error("cache folder copied")
	}
	if _, err := os.Stat(filepath.Join(wp, "wp-content/advanced-cache.php.nostek-off")); err != nil {
		t.Error("drop-in not moved aside")
	}
	got, _ := m.Get(s.ID)
	if got.TablePrefix != "np_" {
		t.Errorf("prefix %q", got.TablePrefix)
	}
	cf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "compose.yml"))
	if strings.Count(string(cf), "WORDPRESS_TABLE_PREFIX: np_") < 2 {
		t.Errorf("compose has no prefix:\n%s", cf)
	}
	loaded := strings.Join(f.stdin, "\n")
	if !strings.Contains(loaded, "INSERT INTO `np_options` VALUES (1);") || strings.Contains(loaded, "USE `cpuser_wp`") || strings.Contains(loaded, "CREATE DATABASE `cpuser_wp`") {
		t.Errorf("database load:\n%s", loaded)
	}
	all := strings.Join(seen, "\n")
	o, _ := strings.CutPrefix(strings.TrimPrefix(strings.TrimPrefix(oldHome, "https:"), "http:"), "")
	for _, want := range []string{"wp search-replace " + o + " //" + hostOf(s.URL), "wp option update home " + s.URL, "wp option delete nostek_migrate_key", "wp plugin delete nostek-migrate"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in:\n%s", want, all)
		}
	}
	b, _ := m.ListBackups(s.ID)
	if len(b.Backups) != 1 || b.Backups[0].Kind != "before-migrate" {
		t.Fatalf("safety backup: %+v", b.Backups)
	}
	steps := ""
	for _, v := range m.jobs.List() {
		if v.Site == s.ID && v.Kind == "migrate" && v.State == "done" {
			steps = strings.Join(v.Steps, "\n")
		}
	}
	if !strings.Contains(steps, "done "+oldHome+" → "+s.URL) {
		t.Errorf("summary:\n%s", steps)
	}
	if strings.Contains(steps, "old-secret") {
		t.Error("old password in the job")
	}
}

func hostOf(u string) string { return strings.TrimPrefix(u, "https://") }

// ---------- the plugin way, against a fake plugin ----------

type fakePlugin struct {
	key      string
	files    map[string]string // path -> content ("->x" = link)
	tables   map[string][]string
	finished bool
	calls    []string
	mu       sync.Mutex
}

func (p *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	route := strings.TrimPrefix(r.URL.Path, "/wp-json/nostek-migrate/v1/")
	p.calls = append(p.calls, r.Method+" "+route)
	if r.Header.Get("X-Nostek-Key") != p.key || p.key == "" {
		w.WriteHeader(403)
		io.WriteString(w, `{"message":"wrong or expired key"}`)
		return
	}
	var names []string
	for n := range p.files {
		names = append(names, n)
	}
	sort.Strings(names)
	switch r.Method + " " + route {
	case "GET info":
		var ts []map[string]any
		for n := range p.tables {
			ts = append(ts, map[string]any{"name": n, "rows": 1, "bytes": 10})
		}
		json.NewEncoder(w).Encode(map[string]any{"plugin": "1.0.0", "wordpress": "6.8.3", "home": "https://old.example.com", "prefix": "np_", "tables": ts})
	case "GET files":
		after := r.URL.Query().Get("after")
		var es []PluginEntry
		for _, n := range names {
			if n <= after {
				continue
			}
			e := PluginEntry{Path: n, Type: "file", Size: int64(len(p.files[n])), Mode: 0o644, MTime: 1700000000}
			if strings.HasPrefix(p.files[n], "->") {
				e = PluginEntry{Path: n, Type: "link", Target: p.files[n][2:]}
			}
			es = append(es, e)
			if len(es) == 2 { // small pages, so paging is used
				break
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"entries": es, "more": len(es) == 2})
	case "POST files":
		var b struct{ Paths []string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		tw := tar.NewWriter(w)
		for _, n := range b.Paths {
			c := p.files[n]
			_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte(c))
		}
		_ = tw.Close()
	case "GET file":
		c := p.files[r.URL.Query().Get("path")]
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n, _ := strconv.Atoi(r.URL.Query().Get("length"))
		io.WriteString(w, c[off:min(off+n, len(c))])
	case "GET table":
		name, after := r.URL.Query().Get("name"), r.URL.Query().Get("after")
		pieces := p.tables[name]
		i := 0
		if after != "" {
			i, _ = strconv.Atoi(after)
		}
		var next any
		if i+1 < len(pieces) {
			next = strconv.Itoa(i + 1)
		}
		json.NewEncoder(w).Encode(map[string]any{"sql": pieces[i], "rows": 1, "next": next})
	case "POST finish":
		p.finished = true
		p.key = ""
		io.WriteString(w, `{"done":true}`)
	default:
		w.WriteHeader(404)
	}
}

func TestMigrateFromPlugin(t *testing.T) {
	m, f, s := migrateSite(t)
	seen := fakeWPCLI(f, oldSiteWP("https://old.example.com"))
	fp := &fakePlugin{key: testKey, files: map[string]string{
		"index.php":                        "<?php // public_html",
		"wp-config.php":                    "<?php define('DB_PASSWORD', 'old-secret'); $table_prefix = 'np_';",
		"wp-includes/version.php":          "<?php $wp_version = '6.8.3';",
		"wp-content/uploads/2026/11/a.jpg": "JPEG",
		"wp-content/cache/page.html":       "cached",
		"wp-content/advanced-cache.php":    "<?php",
		"wp-content/themes/t/link":         "->../../uploads",
	}, tables: map[string][]string{
		"np_options": {"CREATE TABLE `np_options` (id int);\n", "INSERT INTO `np_options` VALUES (1);\n"},
		"other_log":  {"CREATE TABLE `other_log` (id int);\n"},
	}}
	srv, ca := tlsOn(t, "127.0.0.1", fp)
	m.cfg.MigrateTestCA = ca

	// without the test allow-list the loopback "old site" is refused like any private address
	job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "plugin", Code: mkCode(srv.URL, testKey)})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "public internet address") {
		t.Fatalf("guard: %+v", v)
	}
	m.cfg.MigrateTestAllow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	// a wrong key is refused by the plugin, and only one route is tried
	fp.calls = nil
	job, _ = m.StartMigrate(s.ID, MigrateRequest{Way: "plugin", Code: mkCode(srv.URL, strings.Repeat("x", 43))})
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "403") {
		t.Fatalf("wrong key: %+v", v)
	}
	if len(fp.calls) != 1 {
		t.Errorf("wrong key tried %v", fp.calls)
	}

	job, _ = m.StartMigrate(s.ID, MigrateRequest{Way: "plugin", Code: mkCode(srv.URL, testKey)})
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("migrate: %+v", v)
	}
	if !fp.finished {
		t.Error("finish not called")
	}
	for _, c := range fp.calls {
		if strings.Contains(c, "cache/page.html") {
			t.Error("cache fetched")
		}
	}
	for _, sin := range f.stdin {
		if strings.Contains(sin, "other_log") {
			t.Error("a table without the prefix was copied")
		}
	}
	checkMoved(t, m, f, s, *seen, "https://old.example.com", "public_html")
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), testKey) {
			t.Fatal("key in a docker argument list")
		}
	}
}

func TestMigrateLargeFileInRanges(t *testing.T) {
	m, _, s := migrateSite(t)
	big := strings.Repeat("0123456789", 7) // ranges of 16 bytes below
	fp := &fakePlugin{key: testKey, files: map[string]string{"big.bin": big}}
	srv, ca := tlsOn(t, "127.0.0.1", fp)
	m.cfg.MigrateTestCA = ca
	m.cfg.MigrateTestAllow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	code, _ := ParseMigrateCode(mkCode(srv.URL, testKey))
	p, _, err := m.connectPlugin(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for off := int64(0); off < int64(len(big)); {
		n, err := p.rangeTo(context.Background(), "big.bin", off, 16, &b)
		if err != nil || n == 0 {
			t.Fatal(n, err)
		}
		off += n
	}
	if b.String() != big {
		t.Fatalf("ranges %q", b.String())
	}
	_ = s
}

func TestMigrateNative(t *testing.T) {
	m, f, h, _, s := nativeReady(t)
	ticking(m, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	_ = os.WriteFile(filepath.Join(m.wpDir(s.ID), "wp-config.php"), []byte("<?php // the panel's own config"), 0o640)
	h.wp = oldSiteWP("https://cpuser.example.com")
	arc := gz(tarOf(t, append(wpTree("public_html", "cpuser_wp", "np_"), ent{name: "public_html/cpuser_wp.sql.gz", body: string(gz([]byte(oldDump)))})))
	if _, err := m.MigrateUpload(s.ID, "archive", bytes.NewReader(arc)); err != nil {
		t.Fatal(err)
	}
	job, err := m.StartMigrate(s.ID, MigrateRequest{Way: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("migrate %+v\n%s", v, h.all())
	}
	all := h.all()
	for _, want := range []string{"config set table_prefix np_ --type=variable", "search-replace //cpuser.example.com //" + hostOf(s.URL), "systemctl stop " + NativeUnit(s.ID)} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if sql := strings.Join(h.stdin, "\n"); !strings.Contains(sql, "INSERT INTO `np_options` VALUES (1);") || strings.Contains(sql, "USE `cpuser_wp`") {
		t.Errorf("database load:\n%s", sql)
	}
	if strings.Contains(calls(f), "compose") {
		t.Error("native migration reached Docker")
	}
	if b, _ := os.ReadFile(filepath.Join(m.wpDir(s.ID), "index.php")); string(b) != "<?php // public_html" {
		t.Errorf("index.php %q", b)
	}
}
