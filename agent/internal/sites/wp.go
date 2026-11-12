package sites

// WordPress Tools (WP-01): overview, one-click wp-admin login, plugins, themes, core updates, settings, integrity
// check, a WP-CLI console and the system cron. Every WordPress call runs WP-CLI in the site's own `cli` container
// (uid 33, the site's database, outbound internet for updates); nothing runs in the panel or on the host's PHP.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxCLIOut is the most WP-CLI output returned to the panel.
	MaxCLIOut = 64 << 10
	// LoginTTL is how long a one-click login link works (once).
	LoginTTL = 60
	// CronEvery is how often the agent runs wp-cron for sites that use the system cron.
	CronEvery = 5 * time.Minute
	muPlugin  = "/wp-content/mu-plugins/nostek-panel.php"
)

// WPItem is one plugin or theme as WP-CLI lists it.
type WPItem struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Status        string `json:"status"` // active | inactive | active-network | must-use | dropin | parent
	Version       string `json:"version"`
	Update        string `json:"update"` // available | none | ...
	UpdateVersion string `json:"update_version"`
	AutoUpdate    string `json:"auto_update"` // on | off
}

// TLS is the certificate the edge serves for the site's address.
type TLS struct {
	Host    string `json:"host"`
	OK      bool   `json:"ok"`
	Issuer  string `json:"issuer,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	Error   string `json:"error,omitempty"`
}

// WP is the WordPress overview of a site.
type WP struct {
	Version       string   `json:"version"`
	CoreUpdate    string   `json:"core_update"` // newest version offered, "" when up to date or unknown
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Admins        []string `json:"admins"`
	AdminUser     string   `json:"admin_user"`
	Indexing      bool     `json:"indexing"`
	Maintenance   bool     `json:"maintenance"`
	Debug         bool     `json:"debug"`
	SystemCron    bool     `json:"system_cron"`
	FileEditOff   bool     `json:"file_edit_off"`
	SSLAdmin      bool     `json:"ssl_admin"`
	PHP           string   `json:"php"`
	Server        string   `json:"server"`
	Plugins       []WPItem `json:"plugins"`
	Themes        []WPItem `json:"themes"`
	PluginUpdates int      `json:"plugin_updates"`
	ThemeUpdates  int      `json:"theme_updates"`
	TLS           TLS      `json:"tls"`
	Warnings      []string `json:"warnings"`
}

// wpOut is the result of one command in a WP-CLI batch.
type wpOut struct {
	RC  int
	Out string
	Err string
}

// text is stdout and stderr together, as a person reads it.
func (o wpOut) text() string {
	return strings.TrimSpace(strings.TrimSpace(o.Out) + "\n" + strings.TrimSpace(o.Err))
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// wpScript runs each command with its own stdout, stderr and exit code, separated by a marker nobody can guess.
func wpScript(mark string, cmds [][]string) string {
	var b strings.Builder
	b.WriteString("cd /var/www/html || exit 1\n")
	for i, c := range cmds {
		q := make([]string, len(c))
		for j := range c {
			q[j] = shq(c[j])
		}
		fmt.Fprintf(&b, "%s </dev/null >/tmp/np.o 2>/tmp/np.e; rc=$?\necho; echo \"%s %d $rc\"; cat /tmp/np.o; echo; echo \"%s err\"; cat /tmp/np.e\n",
			strings.Join(q, " "), mark, i, mark)
	}
	fmt.Fprintf(&b, "echo; echo \"%s end\"\n", mark)
	return b.String()
}

// parseBatch reads the output of wpScript; anything before the first marker (compose's own lines) is ignored.
func parseBatch(mark string, n int, raw []byte) ([]wpOut, bool) {
	res := make([]wpOut, n)
	cur, inErr, end := -1, false, false
	var out, errb strings.Builder
	flush := func() {
		if cur >= 0 && cur < n {
			res[cur].Out, res[cur].Err = out.String(), errb.String()
		}
		out.Reset()
		errb.Reset()
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, mark+" ") {
			f := strings.Fields(strings.TrimPrefix(line, mark+" "))
			switch {
			case len(f) == 1 && f[0] == "err":
				inErr = true
				continue
			case len(f) == 1 && f[0] == "end":
				flush()
				cur, end = n, true
				continue
			case len(f) == 2:
				i, e1 := strconv.Atoi(f[0])
				rc, e2 := strconv.Atoi(f[1])
				if e1 == nil && e2 == nil && i >= 0 && i < n {
					flush()
					cur, inErr = i, false
					res[i].RC = rc
					continue
				}
			}
		}
		if cur < 0 || cur >= n {
			continue
		}
		if inErr {
			errb.WriteString(line + "\n")
		} else {
			out.WriteString(line + "\n")
		}
	}
	return res, end
}

var wpErrRe = regexp.MustCompile(`(?m)^Error: (.*)$`)

// wpErr turns a failed WP-CLI command into a short message the owner can act on.
func wpErr(o wpOut) error {
	t := o.Err + "\n" + o.Out
	if strings.Contains(t, "Error establishing a database connection") {
		return fmt.Errorf("%w: WordPress cannot reach its database; start the website first", ErrBadInput)
	}
	if m := wpErrRe.FindStringSubmatch(t); m != nil {
		msg := strings.TrimSpace(m[1])
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return fmt.Errorf("%w: %s", ErrBadInput, msg)
	}
	return fmt.Errorf("%w: WP-CLI failed: %s", ErrBadInput, tail([]byte(o.text())))
}

var composeMu sync.Mutex

// syncCompose rewrites the site's compose file when this agent renders it differently (for example the WP-CLI
// container got outbound internet in v1.6.0). Running containers are not touched: changes to web, php or db apply
// at the next start of the website.
func (m *Manager) syncCompose(s Site) error {
	cf, err := s.Compose()
	if err != nil {
		return nil // an old site the catalogue no longer knows keeps its file
	}
	composeMu.Lock()
	defer composeMu.Unlock()
	p := filepath.Join(m.dir(s.ID), "compose.yml")
	if old, err := os.ReadFile(p); err == nil && string(old) == cf {
		return nil
	}
	return writeFile(p, []byte(cf), 0o644)
}

func (m *Manager) wpSite(id string) (Site, error) {
	s, err := m.dbSite(id)
	if err != nil {
		return s, err
	}
	return s, m.syncCompose(s)
}

// wp runs a batch of WP-CLI commands (argument lists, never a shell line from outside) in one cli container.
func (m *Manager) wp(ctx context.Context, id string, env map[string]string, cmds ...[]string) ([]wpOut, error) {
	mark := "@@np-" + randHex(6)
	args := []string{"run", "--rm", "-T", "--no-deps"}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "cli", "sh", "-c", wpScript(mark, cmds))
	raw, err := m.compose(ctx, id, nil, args...)
	res, end := parseBatch(mark, len(cmds), raw)
	if !end {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("WP-CLI took too long and was stopped")
		}
		return nil, fmt.Errorf("WP-CLI could not start: %v %s", err, tail(raw))
	}
	return res, nil
}

// ensureMU writes the panel's must-use plugin (one-click login and maintenance mode) when it is missing or old.
func (m *Manager) ensureMU(id string) error {
	r, err := m.openWP(id)
	if err != nil {
		return err
	}
	cur, rerr := r.ReadFile(strings.TrimPrefix(muPlugin, "/"))
	r.Close()
	if rerr == nil && string(cur) == MUPlugin {
		return nil
	}
	if err := m.Mkdir(id, "/wp-content/mu-plugins"); err != nil && !errors.Is(err, ErrExists) {
		return err
	}
	_, err = m.WriteFile(id, muPlugin, strings.NewReader(MUPlugin), true)
	return err
}

// overviewPHP reads what the dashboard shows in one go. Constant text: nothing from a request goes in.
const overviewPHP = `$a = array();
foreach (get_users(array('role' => 'administrator', 'orderby' => 'ID')) as $u) { $a[] = $u->user_login; }
echo 'NPJSON:' . wp_json_encode(array(
  'version' => get_bloginfo('version'),
  'title' => get_option('blogname'),
  'url' => home_url(),
  'admins' => $a,
  'indexing' => get_option('blog_public') == '1',
  'maintenance' => get_option('nostek_maintenance') == '1',
  'debug' => defined('WP_DEBUG') && WP_DEBUG,
  'system_cron' => defined('DISABLE_WP_CRON') && DISABLE_WP_CRON,
  'file_edit_off' => defined('DISALLOW_FILE_EDIT') && DISALLOW_FILE_EDIT,
  'ssl_admin' => defined('FORCE_SSL_ADMIN') && FORCE_SSL_ADMIN,
)) . "\n";`

var itemFields = "--fields=name,title,status,version,update,update_version,auto_update"

func npJSON(s string, v any) error {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "NPJSON:") {
			return json.Unmarshal([]byte(strings.TrimPrefix(line, "NPJSON:")), v)
		}
	}
	return errors.New("no answer from WordPress")
}

// lastJSON parses the last line of s that starts with c ('[' or '{'): PHP notices may come before it.
func lastJSON(s string, c byte, v any) error {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && l[0] == c {
			return json.Unmarshal([]byte(l), v)
		}
	}
	return errors.New("no list in the answer")
}

// WordPress returns the overview of a site's WordPress.
func (m *Manager) WordPress(ctx context.Context, id string) (WP, error) {
	s, err := m.wpSite(id)
	if err != nil {
		return WP{}, err
	}
	if err := m.ensureMU(id); err != nil {
		return WP{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tlsc := make(chan TLS, 1)
	go func() { tlsc <- m.tlsCheck(ctx, s.Canonical()) }()
	res, err := m.wp(ctx, id, nil,
		[]string{"wp", "eval", overviewPHP},
		[]string{"wp", "plugin", "list", "--format=json", itemFields},
		[]string{"wp", "theme", "list", "--format=json", itemFields},
		[]string{"wp", "core", "check-update", "--format=json"},
	)
	if err != nil {
		return WP{}, err
	}
	w := WP{AdminUser: s.AdminUser, PHP: s.PHP, Server: s.Server, Plugins: []WPItem{}, Themes: []WPItem{}, Warnings: []string{}}
	if res[0].RC != 0 {
		return WP{}, wpErr(res[0])
	}
	if err := npJSON(res[0].Out, &w); err != nil {
		return WP{}, fmt.Errorf("WordPress overview: %v", err)
	}
	w.AdminUser, w.PHP, w.Server = s.AdminUser, s.PHP, s.Server
	if w.Admins == nil {
		w.Admins = []string{}
	}
	if res[1].RC != 0 || lastJSON(res[1].Out, '[', &w.Plugins) != nil {
		w.Warnings = append(w.Warnings, "plugins: "+wpErr(res[1]).Error())
	}
	if res[2].RC != 0 || lastJSON(res[2].Out, '[', &w.Themes) != nil {
		w.Warnings = append(w.Warnings, "themes: "+wpErr(res[2]).Error())
	}
	if res[3].RC != 0 {
		w.Warnings = append(w.Warnings, "core: "+wpErr(res[3]).Error())
	} else {
		var up []struct {
			Version string `json:"version"`
		}
		if lastJSON(res[3].Out, '[', &up) == nil && len(up) > 0 {
			w.CoreUpdate = up[0].Version
		}
	}
	for _, p := range w.Plugins {
		if p.Update == "available" {
			w.PluginUpdates++
		}
	}
	for _, t := range w.Themes {
		if t.Update == "available" {
			w.ThemeUpdates++
		}
	}
	for i := range w.Warnings {
		w.Warnings[i] = strings.Replace(w.Warnings[i], ErrBadInput.Error()+": ", "", 1)
	}
	w.TLS = <-tlsc
	return w, nil
}

// tlsCheck asks the local edge for the site's certificate and checks it like a browser would.
func (m *Manager) tlsCheck(ctx context.Context, host string) TLS {
	t := TLS{Host: host}
	if host == "" {
		t.Error = "no routed address"
		return t
	}
	addr := m.tlsAddr
	if addr == "" {
		addr = "127.0.0.1:443"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: m.tlsRoots}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Error = err.Error()
		var ve *tls.CertificateVerificationError
		if errors.As(err, &ve) && len(ve.UnverifiedCertificates) > 0 {
			t.Issuer = ve.UnverifiedCertificates[0].Issuer.CommonName
			t.Expires = ve.UnverifiedCertificates[0].NotAfter.Unix()
		}
		return t
	}
	defer c.Close()
	st := c.(*tls.Conn).ConnectionState()
	if len(st.PeerCertificates) > 0 {
		pc := st.PeerCertificates[0]
		t.Issuer, t.Expires = pc.Issuer.CommonName, pc.NotAfter.Unix()
	}
	t.OK = true
	return t
}

// Login prepares a one-click wp-admin login and returns the link. The link works once, for LoginTTL seconds;
// only its SHA-256 is stored in WordPress, and the link itself is never logged.
func (m *Manager) Login(ctx context.Context, id string) (string, string, error) {
	s, err := m.wpSite(id)
	if err != nil {
		return "", "", err
	}
	if err := m.ensureMU(id); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tok := randHex(32)
	sum := sha256.Sum256([]byte(tok))
	res, err := m.wp(ctx, id, map[string]string{"NP_HASH": hex.EncodeToString(sum[:]), "NP_USER": s.AdminUser},
		[]string{"wp", "eval", loginPHP})
	if err != nil {
		return "", "", err
	}
	if res[0].RC != 0 {
		return "", "", wpErr(res[0])
	}
	var a struct {
		User string `json:"user"`
	}
	if err := npJSON(res[0].Out, &a); err != nil {
		return "", "", err
	}
	return s.URL + "/?nostek_login=" + tok, a.User, nil
}

const loginPHP = `$u = get_user_by('login', (string) getenv('NP_USER'));
if (!$u || !user_can($u, 'manage_options')) {
  $all = get_users(array('role' => 'administrator', 'orderby' => 'ID', 'number' => 1));
  $u = $all ? $all[0] : null;
}
if (!$u) { WP_CLI::error('there is no administrator account'); }
update_option('nostek_login', wp_json_encode(array('h' => (string) getenv('NP_HASH'), 'u' => $u->user_login, 'e' => time() + 60)), false);
echo 'NPJSON:' . wp_json_encode(array('user' => $u->user_login)) . "\n";`

// MUPlugin is the must-use plugin the agent keeps in every site.
const MUPlugin = `<?php
/*
 * Plugin Name: Nostek Panel
 * Description: One-click login and maintenance mode from Nostek Panel. Managed by the panel: changes are overwritten.
 */
if (!defined('ABSPATH')) { exit; }

add_action('init', function () {
	if (!isset($_GET['nostek_login'])) { return; }
	nocache_headers();
	$raw = (string) wp_unslash($_GET['nostek_login']);
	$d = json_decode((string) get_option('nostek_login', ''), true);
	if (!is_array($d) || empty($d['h']) || empty($d['e']) || time() > (int) $d['e'] || !hash_equals((string) $d['h'], hash('sha256', $raw))) {
		wp_die('This login link is not valid any more. Open WordPress again from Nostek Panel.', 'Login link expired', array('response' => 403));
	}
	delete_option('nostek_login');
	$u = get_user_by('login', (string) $d['u']);
	if (!$u || !user_can($u, 'manage_options')) {
		wp_die('This account cannot manage the site.', 'Login refused', array('response' => 403));
	}
	wp_set_current_user($u->ID);
	wp_set_auth_cookie($u->ID, false, is_ssl());
	do_action('wp_login', $u->user_login, $u);
	wp_safe_redirect(admin_url());
	exit;
}, 1);

add_action('template_redirect', function () {
	if (get_option('nostek_maintenance') !== '1' || current_user_can('manage_options')) { return; }
	status_header(503);
	header('Retry-After: 600');
	nocache_headers();
	$name = esc_html(get_bloginfo('name'));
	echo '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex"><title>' . $name . '</title>'
		. '<style>body{font:16px/1.5 system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;background:#f4f7f7;color:#14302f}main{max-width:32rem;padding:2rem;text-align:center}h1{font-size:1.5rem}</style></head>'
		. '<body><main><h1>' . $name . '</h1><p>We are doing some work on the site and will be back shortly.</p><p lang="id">Situs sedang dalam perawatan dan akan segera kembali.</p></main></body></html>';
	exit;
}, 0);
`

var (
	// a plugin or theme name as WP-CLI uses it (folder or file name); never starts with "-".
	itemRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	// a slug on wordpress.org
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
)

// ItemAction is a change to plugins or themes.
type ItemAction struct {
	Do       string   `json:"do"`       // activate | deactivate | update | delete | install | auto-on | auto-off
	Names    []string `json:"names"`    // plugin or theme names; for install, wordpress.org slugs
	All      bool     `json:"all"`      // update, auto-on, auto-off: every item
	Activate bool     `json:"activate"` // install: activate as well
}

// itemCmds builds the WP-CLI commands for an item action; kind is "plugin" or "theme".
func itemCmds(kind string, a ItemAction) ([][]string, error) {
	if len(a.Names) > 50 {
		return nil, fmt.Errorf("%w: at most 50 at a time", ErrBadInput)
	}
	re := itemRe
	if a.Do == "install" {
		re = slugRe
	}
	for _, n := range a.Names {
		if !re.MatchString(n) {
			return nil, fmt.Errorf("%w: %q is not a valid %s name", ErrBadInput, n, kind)
		}
	}
	allOK := a.Do == "update" || a.Do == "auto-on" || a.Do == "auto-off"
	if a.All && !allOK {
		return nil, fmt.Errorf("%w: %s cannot apply to all", ErrBadInput, a.Do)
	}
	if a.All && len(a.Names) > 0 {
		return nil, fmt.Errorf("%w: give names or all, not both", ErrBadInput)
	}
	if !a.All && len(a.Names) == 0 {
		return nil, fmt.Errorf("%w: choose at least one %s", ErrBadInput, kind)
	}
	if a.Activate && a.Do != "install" {
		return nil, fmt.Errorf("%w: activate goes with install only", ErrBadInput)
	}
	target := a.Names
	if a.All {
		target = []string{"--all"}
	}
	c := func(args ...string) []string { return append(append([]string{"wp", kind}, args...), target...) }
	switch a.Do {
	case "activate":
		if kind == "theme" && len(a.Names) != 1 {
			return nil, fmt.Errorf("%w: only one theme can be active", ErrBadInput)
		}
		return [][]string{c("activate")}, nil
	case "deactivate":
		if kind == "theme" {
			return nil, fmt.Errorf("%w: activate another theme instead", ErrBadInput)
		}
		return [][]string{c("deactivate")}, nil
	case "update":
		return [][]string{c("update")}, nil
	case "delete":
		if kind == "plugin" {
			return [][]string{c("uninstall", "--deactivate")}, nil
		}
		return [][]string{c("delete")}, nil
	case "install":
		if a.Activate {
			return [][]string{c("install", "--activate")}, nil
		}
		return [][]string{c("install")}, nil
	case "auto-on":
		return [][]string{c("auto-updates", "enable")}, nil
	case "auto-off":
		return [][]string{c("auto-updates", "disable")}, nil
	}
	return nil, fmt.Errorf("%w: unknown action %q", ErrBadInput, a.Do)
}

// limitOut keeps the end of long output (the summary lines are last).
func limitOut(s string) string {
	if len(s) > MaxCLIOut {
		s = "...\n" + s[len(s)-MaxCLIOut:]
	}
	return s
}

// runChange runs a batch that changes the site, one change at a time per agent; it stops at the first failure.
func (m *Manager) runChange(ctx context.Context, id string, d time.Duration, cmds ...[]string) (string, error) {
	if _, err := m.wpSite(id); err != nil {
		return "", err
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	res, err := m.wp(ctx, id, nil, cmds...)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, o := range res {
		b.WriteString(o.text() + "\n")
		if o.RC != 0 {
			return "", wpErr(o)
		}
	}
	return limitOut(strings.TrimSpace(b.String())), nil
}

// Items changes plugins or themes and returns WP-CLI's report.
func (m *Manager) Items(ctx context.Context, id, kind string, a ItemAction) (string, error) {
	if kind != "plugin" && kind != "theme" {
		return "", fmt.Errorf("%w: unknown kind", ErrBadInput)
	}
	cmds, err := itemCmds(kind, a)
	if err != nil {
		return "", err
	}
	return m.runChange(ctx, id, 10*time.Minute, cmds...)
}

// Core updates WordPress ("update") or WordPress, every plugin and every theme ("all").
func (m *Manager) Core(ctx context.Context, id, do string) (string, error) {
	cmds := [][]string{{"wp", "core", "update"}, {"wp", "core", "update-db"}}
	switch do {
	case "update":
	case "all":
		cmds = append(cmds, []string{"wp", "plugin", "update", "--all"}, []string{"wp", "theme", "update", "--all"})
	default:
		return "", fmt.Errorf("%w: unknown action %q", ErrBadInput, do)
	}
	return m.runChange(ctx, id, 15*time.Minute, cmds...)
}

// Setting names the dashboard can switch.
var Settings = []string{"indexing", "maintenance", "debug", "system_cron"}

func settingCmds(key string, on bool) ([][]string, error) {
	b := map[bool]string{true: "true", false: "false"}[on]
	n := map[bool]string{true: "1", false: "0"}[on]
	cfg := func(name, v string) []string {
		return []string{"wp", "config", "set", name, v, "--raw", "--type=constant"}
	}
	switch key {
	case "indexing":
		return [][]string{{"wp", "option", "update", "blog_public", n}}, nil
	case "maintenance":
		return [][]string{{"wp", "option", "update", "nostek_maintenance", n}}, nil
	case "debug":
		// Errors go to the container log (the Logs card), never to visitors or a public debug.log.
		if on {
			return [][]string{cfg("WP_DEBUG", "true"), cfg("WP_DEBUG_LOG", "'/dev/stderr'"), cfg("WP_DEBUG_DISPLAY", "false")}, nil
		}
		return [][]string{cfg("WP_DEBUG", "false"), cfg("WP_DEBUG_LOG", "false")}, nil
	case "system_cron":
		return [][]string{cfg("DISABLE_WP_CRON", b)}, nil
	}
	return nil, fmt.Errorf("%w: unknown setting %q", ErrBadInput, key)
}

// Set switches one dashboard setting.
func (m *Manager) Set(ctx context.Context, id, key string, on bool) error {
	cmds, err := settingCmds(key, on)
	if err != nil {
		return err
	}
	if key == "maintenance" {
		if _, err := m.wpSite(id); err != nil {
			return err
		}
		if err := m.ensureMU(id); err != nil {
			return err
		}
	}
	if _, err := m.runChange(ctx, id, 2*time.Minute, cmds...); err != nil {
		return err
	}
	if key != "system_cron" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	s.SystemCron = on
	return writeSite(m.dir(id), s)
}

// Check is the result of a WordPress integrity check.
type Check struct {
	CoreOK    bool   `json:"core_ok"`
	Core      string `json:"core"`
	PluginsOK bool   `json:"plugins_ok"`
	Plugins   string `json:"plugins"`
}

// Integrity compares WordPress and the plugins from wordpress.org with their published checksums.
func (m *Manager) Integrity(ctx context.Context, id string) (Check, error) {
	if _, err := m.wpSite(id); err != nil {
		return Check{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := m.wp(ctx, id, nil,
		[]string{"wp", "core", "verify-checksums"},
		[]string{"wp", "plugin", "verify-checksums", "--all"})
	if err != nil {
		return Check{}, err
	}
	if strings.Contains(res[0].text(), "Error establishing a database connection") {
		return Check{}, wpErr(res[0])
	}
	return Check{CoreOK: res[0].RC == 0, Core: limitOut(res[0].text()), PluginsOK: res[1].RC == 0, Plugins: limitOut(res[1].text())}, nil
}

// SplitArgs splits a console line into arguments like a shell would for quotes and backslashes, without any
// expansion. A leading "wp" is dropped.
func SplitArgs(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	have, quote, esc := false, rune(0), false
	for _, c := range line {
		switch {
		case esc:
			cur.WriteRune(c)
			esc = false
		case c == '\\' && quote != '\'':
			esc, have = true, true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, have = c, true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	if quote != 0 || esc {
		return nil, fmt.Errorf("%w: a quote is not closed", ErrBadInput)
	}
	if have {
		args = append(args, cur.String())
	}
	if len(args) > 0 && args[0] == "wp" {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("%w: type a WP-CLI command, for example: plugin list", ErrBadInput)
	}
	return args, nil
}

// cliRefused lists what the console does not run: interactive commands and flags that reach other machines or paths.
func cliRefused(args []string) string {
	switch args[0] {
	case "shell", "server", "cli":
		return "wp " + args[0] + " is not available in the panel"
	}
	if len(args) > 1 && args[0] == "db" && (args[1] == "cli" || args[1] == "shell") {
		return "wp db " + args[1] + " is interactive; use the Database tab"
	}
	for _, a := range args {
		for _, f := range []string{"--ssh", "--http", "--path"} {
			if a == f || strings.HasPrefix(a, f+"=") {
				return f + " is not available in the panel"
			}
		}
	}
	return ""
}

// CLIResult is one console run.
type CLIResult struct {
	Code   int    `json:"code"`
	Output string `json:"output"`
}

// CLI runs one WP-CLI command line in the site's cli container (uid 33, no shell).
func (m *Manager) CLI(ctx context.Context, id, line string) (CLIResult, error) {
	if len(line) > 2000 {
		return CLIResult{}, fmt.Errorf("%w: the command is too long", ErrBadInput)
	}
	args, err := SplitArgs(line)
	if err != nil {
		return CLIResult{}, err
	}
	if why := cliRefused(args); why != "" {
		return CLIResult{}, fmt.Errorf("%w: %s", ErrBadInput, why)
	}
	if _, err := m.wpSite(id); err != nil {
		return CLIResult{}, err
	}
	if !m.mu.TryLock() {
		return CLIResult{}, ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := m.wp(ctx, id, nil, append([]string{"wp"}, args...))
	if err != nil {
		return CLIResult{}, err
	}
	return CLIResult{Code: res[0].RC, Output: limitOut(res[0].text())}, nil
}

// RunCron runs wp-cron.php for every ready site that uses the system cron, inside its own PHP container.
func (m *Manager) RunCron(ctx context.Context) []string {
	list, err := m.List()
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string
	for _, s := range list {
		if !s.SystemCron || s.Phase != "ready" {
			continue
		}
		svc := "web"
		if s.Server == "nginx" {
			svc = "php"
		}
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		out, err := m.compose(c, s.ID, nil, "exec", "-T", "-u", "33:33", svc, "php", "-d", "max_execution_time=240", "/var/www/html/wp-cron.php")
		cancel()
		if err != nil && !bytes.Contains(out, []byte("is not running")) {
			errs = append(errs, s.ID+": "+tail(out))
		}
	}
	return errs
}

// CronLoop calls RunCron every CronEvery until ctx ends.
func (m *Manager) CronLoop(ctx context.Context, logf func(string, ...any)) {
	t := time.NewTicker(CronEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, e := range m.RunCron(ctx) {
				logf("system cron: %s", e)
			}
		}
	}
}
