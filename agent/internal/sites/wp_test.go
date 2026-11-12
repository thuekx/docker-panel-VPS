package sites

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWP is a stand-in for the wp binary: it answers the few commands the tests use.
const fakeWP = `#!/bin/sh
case "$1 $2" in
"eval "*nostek_login*) [ -n "$NP_HASH" ] || { echo "Error: no hash" >&2; exit 1; }
  echo "$NP_HASH" > "$NP_SEEN"; echo "NPJSON:{\"user\":\"$NP_USER\"}";;
"eval "*) echo "PHP Notice: something"; echo 'NPJSON:{"version":"6.9","title":"Voltix","url":"https://voltix.id","admins":["admin-abc","ops"],"indexing":true,"maintenance":false,"debug":false,"system_cron":false,"file_edit_off":true,"ssl_admin":true}';;
"plugin list") echo '[{"name":"akismet","title":"Akismet","status":"inactive","version":"5.3","update":"available","update_version":"5.4","auto_update":"off"},{"name":"nostek-panel","title":"Nostek Panel","status":"must-use","version":"","update":"none","update_version":"","auto_update":"off"}]';;
"theme list") echo "Warning: could not reach wordpress.org" >&2; echo '[{"name":"twentytwentyfive","title":"Twenty Twenty-Five","status":"active","version":"1.3","update":"none","update_version":"","auto_update":"on"}]';;
"core check-update") echo '[{"version":"6.9.1","update_type":"minor","package_url":"x"}]';;
"core update") echo "Success: WordPress updated successfully.";;
"core update-db") echo "Success: WordPress database already at latest db version.";;
"plugin update") echo "Success: Updated 1 of 1 plugins.";;
"theme update") echo "Error: theme broken" >&2; exit 1;;
"plugin uninstall") echo "Uninstalled and deleted '$4' plugin.";;
"option update") echo "Success: Updated '$3' option.";;
"config set") echo "Success: Updated the constant '$3' in the 'wp-config.php' file with the raw value '$4'.";;
"user list") printf 'it''s "quoted" %s\n' "$*";;
*) echo "Error: '$1' is not a registered wp command." >&2; exit 1;;
esac
`

// wpSite is a ready site whose cli runs are executed by sh with the fake wp first in PATH.
func wpSite(t *testing.T) (*Manager, *fakeRunner, string, string) {
	t.Helper()
	m, f, id := readySite(t)
	if err := os.MkdirAll(filepath.Join(m.wpDir(id), "wp-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "wp"), []byte(fakeWP), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(t.TempDir(), "seen")
	f.hook = func(args []string) ([]byte, error, bool) {
		i := -1
		for k, a := range args {
			if a == "cli" && k+3 < len(args) && args[k+1] == "sh" && args[k+2] == "-c" {
				i = k
			}
		}
		if i < 0 {
			return nil, nil, false
		}
		script := strings.Replace(args[i+3], "cd /var/www/html || exit 1", "true", 1)
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "NP_SEEN=" + seen}
		for k := 0; k < i; k++ {
			if args[k] == "-e" {
				cmd.Env = append(cmd.Env, args[k+1])
			}
		}
		out, err := cmd.CombinedOutput()
		return append([]byte(" Network np_out Creating\n"), out...), err, true
	}
	return m, f, id, seen
}

func TestBatchKeepsEachCommandApart(t *testing.T) {
	cmds := [][]string{{"echo", "a'b c"}, {"sh", "-c", "echo out; echo bad >&2; exit 3"}, {"printf", "no newline"}}
	mark := "@@np-test"
	out, err := exec.Command("sh", "-c", strings.Replace(wpScript(mark, cmds), "cd /var/www/html || exit 1", "true", 1)).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	res, end := parseBatch(mark, len(cmds), append([]byte("noise before\n"), out...))
	if !end {
		t.Fatal("end marker missing")
	}
	if strings.TrimSpace(res[0].Out) != "a'b c" || res[0].RC != 0 {
		t.Errorf("quoting: %+v", res[0])
	}
	if res[1].RC != 3 || strings.TrimSpace(res[1].Out) != "out" || strings.TrimSpace(res[1].Err) != "bad" {
		t.Errorf("rc/stderr: %+v", res[1])
	}
	if strings.TrimSpace(res[2].Out) != "no newline" {
		t.Errorf("last: %+v", res[2])
	}
	if _, end := parseBatch(mark, 1, []byte("container failed\n")); end {
		t.Error("output without markers counted as complete")
	}
}

func TestComposeGivesCLIInternetAndIsSynced(t *testing.T) {
	m, _, id := readySite(t)
	p := filepath.Join(m.dir(id), "compose.yml")
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "networks: [back, out]") || !strings.Contains(string(b), "  out: {}") {
		t.Fatal("cli has no outbound network")
	}
	// an older agent's file is brought up to date before WP-CLI runs
	old := strings.Replace(string(b), "networks: [back, out]", "networks: [back]", 1)
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(id)
	if err := m.syncCompose(s); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(p); string(b2) != string(b) {
		t.Fatal("compose file not rewritten")
	}
}

func TestWordPressOverview(t *testing.T) {
	m, _, id, _ := wpSite(t)
	w, err := m.WordPress(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Version != "6.9" || w.CoreUpdate != "6.9.1" || w.Title != "Voltix" || !w.Indexing || !w.FileEditOff || len(w.Admins) != 2 {
		t.Errorf("overview: %+v", w)
	}
	if len(w.Plugins) != 2 || w.PluginUpdates != 1 || len(w.Themes) != 1 || w.ThemeUpdates != 0 || w.Themes[0].AutoUpdate != "on" {
		t.Errorf("items: %+v %+v", w.Plugins, w.Themes)
	}
	if len(w.Warnings) != 0 {
		t.Errorf("warnings: %v", w.Warnings)
	}
	if w.TLS.OK || w.TLS.Error == "" {
		t.Errorf("tls without an edge: %+v", w.TLS)
	}
	b, err := os.ReadFile(filepath.Join(m.wpDir(id), "wp-content/mu-plugins/nostek-panel.php"))
	if err != nil || string(b) != MUPlugin {
		t.Fatal("must-use plugin not written", err)
	}
}

func TestWordPressNeedsDatabase(t *testing.T) {
	m, f, id, _ := wpSite(t)
	f.hook = func(args []string) ([]byte, error, bool) {
		for i, a := range args {
			if a == "-c" && i+1 < len(args) {
				mark := strings.Fields(args[i+1][strings.Index(args[i+1], "@@np-"):])[0]
				mark = strings.Trim(mark, `"`)
				return []byte("\n" + mark + " 0 1\n\n" + mark + " err\nError: Error establishing a database connection.\n" + mark + " end\n"), nil, true
			}
		}
		return nil, nil, false
	}
	_, err := m.WordPress(context.Background(), id)
	if !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "start the website") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginStoresOnlyTheHash(t *testing.T) {
	m, f, id, seen := wpSite(t)
	link, user, err := m.Login(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(id)
	if user != s.AdminUser || !strings.HasPrefix(link, s.URL+"/?nostek_login=") {
		t.Fatalf("link %s user %s", link, user)
	}
	tok := strings.TrimPrefix(link, s.URL+"/?nostek_login=")
	sum := sha256.Sum256([]byte(tok))
	got, _ := os.ReadFile(seen)
	if strings.TrimSpace(string(got)) != hex.EncodeToString(sum[:]) {
		t.Fatal("WordPress did not get the token's hash")
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), tok) {
			t.Fatal("token in a docker argument list")
		}
	}
	if !strings.Contains(MUPlugin, "hash_equals") || !strings.Contains(MUPlugin, "delete_option('nostek_login')") {
		t.Fatal("must-use plugin does not check and spend the token")
	}
}

func TestItemCommands(t *testing.T) {
	ok := map[string][]string{
		"plugin update all":    {"wp plugin update --all"},
		"plugin delete":        {"wp plugin uninstall --deactivate akismet"},
		"theme activate":       {"wp theme activate akismet"},
		"plugin install":       {"wp plugin install --activate akismet"},
		"plugin auto-on":       {"wp plugin auto-updates enable akismet"},
		"theme auto-off all":   {"wp theme auto-updates disable --all"},
		"plugin deactivate x2": {"wp plugin deactivate akismet hello.php"},
	}
	acts := map[string]struct {
		kind string
		a    ItemAction
	}{
		"plugin update all":    {"plugin", ItemAction{Do: "update", All: true}},
		"plugin delete":        {"plugin", ItemAction{Do: "delete", Names: []string{"akismet"}}},
		"theme activate":       {"theme", ItemAction{Do: "activate", Names: []string{"akismet"}}},
		"plugin install":       {"plugin", ItemAction{Do: "install", Names: []string{"akismet"}, Activate: true}},
		"plugin auto-on":       {"plugin", ItemAction{Do: "auto-on", Names: []string{"akismet"}}},
		"theme auto-off all":   {"theme", ItemAction{Do: "auto-off", All: true}},
		"plugin deactivate x2": {"plugin", ItemAction{Do: "deactivate", Names: []string{"akismet", "hello.php"}}},
	}
	for k, v := range acts {
		c, err := itemCmds(v.kind, v.a)
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if strings.Join(c[0], " ") != ok[k][0] {
			t.Errorf("%s: %v", k, c)
		}
	}
	bad := []struct {
		kind string
		a    ItemAction
	}{
		{"plugin", ItemAction{Do: "activate", Names: []string{"--require=/tmp/x.php"}}},
		{"plugin", ItemAction{Do: "activate", Names: []string{"-x"}}},
		{"plugin", ItemAction{Do: "delete", Names: []string{"../wp-config"}}},
		{"plugin", ItemAction{Do: "install", Names: []string{"https://evil.example/x.zip"}}},
		{"plugin", ItemAction{Do: "delete", All: true}},
		{"plugin", ItemAction{Do: "update"}},
		{"plugin", ItemAction{Do: "update", All: true, Names: []string{"akismet"}}},
		{"theme", ItemAction{Do: "activate", Names: []string{"a", "b"}}},
		{"theme", ItemAction{Do: "deactivate", Names: []string{"a"}}},
		{"plugin", ItemAction{Do: "activate", Names: []string{"a"}, Activate: true}},
		{"plugin", ItemAction{Do: "eval", Names: []string{"a"}}},
	}
	for _, b := range bad {
		if _, err := itemCmds(b.kind, b.a); !errors.Is(err, ErrBadInput) {
			t.Errorf("accepted %+v", b)
		}
	}
}

func TestChangesRunAndReport(t *testing.T) {
	m, _, id, _ := wpSite(t)
	out, err := m.Items(context.Background(), id, "plugin", ItemAction{Do: "update", Names: []string{"akismet"}})
	if err != nil || !strings.Contains(out, "Updated 1 of 1") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := m.Core(context.Background(), id, "update"); err != nil {
		t.Fatal(err)
	}
	// "all" stops at the theme failure and says why
	if _, err := m.Core(context.Background(), id, "all"); err == nil || !strings.Contains(err.Error(), "theme broken") {
		t.Fatalf("err = %v", err)
	}
	if _, err := m.Core(context.Background(), id, "rollback"); !errors.Is(err, ErrBadInput) {
		t.Fatal("unknown core action accepted")
	}
	m.mu.Lock()
	_, err = m.Items(context.Background(), id, "plugin", ItemAction{Do: "update", All: true})
	m.mu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("no busy while another change runs: %v", err)
	}
}

func TestSettings(t *testing.T) {
	m, _, id, _ := wpSite(t)
	for _, k := range Settings {
		if err := m.Set(context.Background(), id, k, true); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	s, _ := m.Get(id)
	if !s.SystemCron {
		t.Fatal("system cron not remembered")
	}
	if err := m.Set(context.Background(), id, "system_cron", false); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(id); s.SystemCron {
		t.Fatal("system cron not cleared")
	}
	c, _ := settingCmds("debug", true)
	if strings.Join(c[1], " ") != "wp config set WP_DEBUG_LOG '/dev/stderr' --raw --type=constant" {
		t.Errorf("debug log goes to %v", c[1])
	}
	if err := m.Set(context.Background(), id, "siteurl", true); !errors.Is(err, ErrBadInput) {
		t.Fatal("unknown setting accepted")
	}
}

func TestRunCronOnlyForSystemCronSites(t *testing.T) {
	m, f, id, _ := wpSite(t)
	if errs := m.RunCron(context.Background()); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), "wp-cron.php") {
			t.Fatal("cron ran for a site without system cron")
		}
	}
	if err := m.Set(context.Background(), id, "system_cron", true); err != nil {
		t.Fatal(err)
	}
	m.RunCron(context.Background())
	found := false
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "exec -T -u 33:33 web php -d max_execution_time=240 /var/www/html/wp-cron.php") {
			found = true
		}
	}
	if !found {
		t.Fatal("cron did not run in the web container")
	}
}

func TestSplitArgsAndConsole(t *testing.T) {
	for in, want := range map[string]string{
		`wp plugin list --status=active`:        "plugin|list|--status=active",
		`option update blogname "My site"`:      "option|update|blogname|My site",
		`post create --post_title="It's \"x\""`: `post|create|--post_title=It's "x"`,
		`a 'b\c'`:                               `a|b\c`,
		`search-replace a\ b c`:                 "search-replace|a b|c",
		`eval 'echo "$HOME";'`:                  `eval|echo "$HOME";`,
	} {
		got, err := SplitArgs(in)
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("%s -> %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "wp", `option get "x`, `a\`} {
		if _, err := SplitArgs(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	for _, in := range []string{"shell", "db cli", "plugin list --ssh=root@host", "option get x --http=https://x", "core version --path=/etc"} {
		a, _ := SplitArgs(in)
		if cliRefused(a) == "" {
			t.Errorf("console runs %q", in)
		}
	}
	m, _, id, _ := wpSite(t)
	r, err := m.CLI(context.Background(), id, `user list "it's" $(id)`)
	if err != nil || r.Code != 0 || !strings.Contains(r.Output, `user list it's $(id)`) {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = m.CLI(context.Background(), id, "nosuch")
	if err != nil || r.Code != 1 || !strings.Contains(r.Output, "not a registered") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTLSCheck(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	m, _ := newTest(t, nil)
	m.tlsAddr = srv.Listener.Addr().String()
	if r := m.tlsCheck(context.Background(), "example.com"); r.OK || r.Error == "" || r.Expires == 0 {
		t.Errorf("untrusted certificate: %+v", r)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	m.tlsRoots = pool
	if r := m.tlsCheck(context.Background(), "example.com"); !r.OK || r.Expires == 0 {
		t.Errorf("trusted certificate: %+v", r)
	}
	if r := m.tlsCheck(context.Background(), "voltix.id"); r.OK {
		t.Errorf("wrong name accepted: %+v", r)
	}
	_ = tls.VersionTLS12
}
