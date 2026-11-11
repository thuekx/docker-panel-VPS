package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
	"github.com/thuekx/docker-panel-VPS/panel/internal/sysinfo"
	webassets "github.com/thuekx/docker-panel-VPS/panel/web"
)

type env struct {
	t     *testing.T
	st    *store.Store
	srv   *Server
	ts    *httptest.Server
	c     *http.Client
	clock time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(dir, "proc")
	os.MkdirAll(proc, 0o755)
	os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal: 4000000 kB\nMemAvailable: 1000000 kB\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0.50 0.40 0.30 1/100 1\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "cpuinfo"), []byte("processor\t: 0\nprocessor\t: 1\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "uptime"), []byte("7200.00 100.00\n"), 0o644)
	srv, err := New(Config{DefaultLang: "en", Version: "test"}, st, webassets.FS, sysinfo.Reader{ProcDir: proc, DiskPath: dir, Hostname: "test-host", BackupFile: filepath.Join(dir, "none")})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, srv: srv, clock: time.Unix(1700000000, 0)}
	srv.now = func() time.Time { return e.clock }
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(func() { e.ts.Close(); st.Close() })
	e.c = e.client()
	return e
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) get(c *http.Client, path string) (int, string, http.Header) {
	resp, err := c.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String(), resp.Header
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (e *env) post(c *http.Client, path string, form url.Values, csrfFrom string) (int, string, http.Header) {
	_, body, _ := e.get(c, csrfFrom)
	return e.postWith(c, path, form, body)
}

// postWith takes the CSRF token from a page already loaded (loading the enrol page again would renew the secret).
func (e *env) postWith(c *http.Client, path string, form url.Values, body string) (int, string, http.Header) {
	m := csrfRe.FindStringSubmatch(body)
	if m != nil {
		form.Set("csrf", m[1])
	}
	resp, err := c.PostForm(e.ts.URL+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String(), resp.Header
}

const pw = "fake-Admin-pass-0123"

// setupAdmin runs the full first-admin flow and returns the TOTP secret and recovery codes.
func (e *env) setupAdmin(c *http.Client, email string) (string, []string) {
	link, err := SetupLink(e.st, e.ts.URL, e.clock)
	if err != nil {
		e.t.Fatal(err)
	}
	path := strings.TrimPrefix(link, e.ts.URL)
	code, _, h := e.post(c, path, url.Values{"email": {email}, "password": {pw}, "password2": {pw}}, path)
	if code != 303 || h.Get("Location") != "/2fa/enrol" {
		e.t.Fatalf("setup: %d %s", code, h.Get("Location"))
	}
	_, body, _ := e.get(c, "/2fa/enrol")
	sec := regexp.MustCompile(`class="secret">([A-Z2-7]+)<`).FindStringSubmatch(body)
	if sec == nil {
		e.t.Fatal("no secret on enrol page")
	}
	totp, _ := auth.TOTPCode(sec[1], e.clock)
	code, body, _ = e.postWith(c, "/2fa/enrol", url.Values{"code": {totp}}, body)
	if code != 200 || !strings.Contains(body, "recovery codes") {
		e.t.Fatalf("enrol: %d", code)
	}
	codes := regexp.MustCompile(`<code>([a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4})</code>`).FindAllStringSubmatch(body, -1)
	var out []string
	for _, m := range codes {
		out = append(out, m[1])
	}
	return sec[1], out
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	if code, _, h := e.get(e.c, "/"); code != 303 || h.Get("Location") != "/login" {
		t.Fatal("dashboard must need login")
	}
	secret, codes := e.setupAdmin(e.c, "admin@example.test")
	if len(codes) != 10 {
		t.Fatalf("want 10 recovery codes, got %d", len(codes))
	}
	code, body, h := e.get(e.c, "/")
	if code != 200 || !strings.Contains(body, "test-host") || !strings.Contains(body, "75") {
		t.Fatalf("dashboard: %d", code)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") || h.Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers missing")
	}
	if !strings.Contains(body, "Unknown") {
		t.Fatal("missing backup must show Unknown, not OK")
	}
	if strings.Contains(body, "WIB") == false {
		t.Fatal("time zone not shown")
	}
	// setup link cannot be reused / a second admin cannot be created that way
	if _, err := SetupLink(e.st, e.ts.URL, e.clock); err == nil {
		t.Fatal("second setup link allowed")
	}
	// logout, then login with password + TOTP
	e.post(e.c, "/logout", url.Values{}, "/")
	if code, _, _ := e.get(e.c, "/"); code != 303 {
		t.Fatal("still logged in after logout")
	}
	e.clock = e.clock.Add(2 * time.Minute)
	code, _, h = e.post(e.c, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login")
	if code != 303 || h.Get("Location") != "/login/2fa" {
		t.Fatalf("login: %d %s", code, h.Get("Location"))
	}
	if code, _, h := e.get(e.c, "/"); code != 303 || h.Get("Location") != "/login/2fa" {
		t.Fatal("dashboard reachable before 2FA")
	}
	totp, _ := auth.TOTPCode(secret, e.clock)
	code, _, h = e.post(e.c, "/login/2fa", url.Values{"code": {totp}}, "/login/2fa")
	if code != 303 || h.Get("Location") != "/" {
		t.Fatalf("2fa: %d", code)
	}
	// same TOTP code cannot be replayed in another session
	c2 := e.client()
	e.post(c2, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login")
	if code, _, _ := e.post(c2, "/login/2fa", url.Values{"code": {totp}}, "/login/2fa"); code != 401 {
		t.Fatalf("replayed TOTP accepted: %d", code)
	}
	// a recovery code works once
	if code, _, _ := e.post(c2, "/login/2fa", url.Values{"code": {strings.ToUpper(codes[0])}}, "/login/2fa"); code != 303 {
		t.Fatal("recovery code refused")
	}
	c3 := e.client()
	e.post(c3, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login")
	if code, _, _ := e.post(c3, "/login/2fa", url.Values{"code": {codes[0]}}, "/login/2fa"); code != 401 {
		t.Fatal("recovery code reused")
	}
	// audit log records the events
	_, body, _ = e.get(e.c, "/audit")
	for _, a := range []string{"admin.created", "2fa.enabled", "login.ok", "login.recovery_code", "login.2fa_failed"} {
		if !strings.Contains(body, a) {
			t.Fatalf("audit missing %s", a)
		}
	}
}

func TestCSRFRequired(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	resp, _ := e.c.PostForm(e.ts.URL+"/settings", url.Values{"timezone": {"UTC"}})
	if resp.StatusCode != 403 {
		t.Fatalf("POST without CSRF: %d", resp.StatusCode)
	}
	resp, _ = e.c.PostForm(e.ts.URL+"/login", url.Values{"email": {"a@b"}, "password": {"x"}})
	if resp.StatusCode != 403 {
		t.Fatalf("login without CSRF: %d", resp.StatusCode)
	}
}

func TestWrongPasswordAndLockout(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	c := e.client()
	for i := 0; i < maxFails; i++ {
		e.ipLimitReset()
		if code, body, _ := e.post(c, "/login", url.Values{"email": {"admin@example.test"}, "password": {"wrong-password-123"}}, "/login"); code != 401 || !strings.Contains(body, "wrong") {
			t.Fatalf("wrong password: %d", code)
		}
	}
	e.ipLimitReset()
	if code, _, _ := e.post(c, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login"); code != 401 {
		t.Fatal("locked account accepted the right password")
	}
	e.clock = e.clock.Add(lockFor + time.Minute)
	e.ipLimitReset()
	if code, _, _ := e.post(c, "/login", url.Values{"email": {"admin@example.test"}, "password": {pw}}, "/login"); code != 303 {
		t.Fatal("lock did not expire")
	}
	// unknown user gets the same answer
	e.ipLimitReset()
	if code, body, _ := e.post(e.client(), "/login", url.Values{"email": {"nobody@example.test"}, "password": {pw}}, "/login"); code != 401 || !strings.Contains(body, "E-mail or password is wrong") {
		t.Fatal("unknown user answer differs")
	}
}

func (e *env) ipLimitReset() { e.srv.ipLimit = auth.NewLimiter(20, time.Minute) }

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	var last int
	for i := 0; i < 25; i++ {
		last, _, _ = e.post(c, "/login", url.Values{"email": {"x@example.test"}, "password": {"whatever-123456"}}, "/login")
	}
	if last != 429 {
		t.Fatalf("no rate limit: %d", last)
	}
}

func TestAdminRotateRevealOnce(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	link, err := AdminRotate(e.st, "admin@example.test", e.ts.URL, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get(e.c, "/"); code != 303 {
		t.Fatal("old session survived the rotate")
	}
	path := strings.TrimPrefix(link, e.ts.URL)
	c := e.client()
	code, body, _ := e.post(c, path, url.Values{}, path)
	m := regexp.MustCompile(`<code id="pw">([^<]+)</code>`).FindStringSubmatch(body)
	if code != 200 || m == nil || len(m[1]) != 20 {
		t.Fatalf("reveal: %d", code)
	}
	if code, _, _ := e.get(c, path); code != 410 {
		t.Fatal("reveal link usable twice")
	}
	e.ipLimitReset()
	if code, _, h := e.post(e.client(), "/login", url.Values{"email": {"admin@example.test"}, "password": {m[1]}}, "/login"); code != 303 || h.Get("Location") != "/login/2fa" {
		t.Fatal("new password does not work")
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM tokens WHERE sealed IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatal("sealed password kept after use")
	}
}

func TestTenantIsolation404(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	h, _ := auth.HashPassword("other-org-password-1")
	other, _ := e.st.CreateOrgWithAdmin("Other", "other@example.test", h)
	e.st.Audit(other.OrgID, other.ID, other.Email, "secret.action", "other org data", "1.2.3.4")
	var id int64
	e.st.DB.QueryRow(`SELECT id FROM audit WHERE action='secret.action'`).Scan(&id)
	code, body, _ := e.get(e.c, "/audit/"+itoa(id))
	if code != 404 || strings.Contains(body, "other org data") {
		t.Fatalf("other org entry visible: %d", code)
	}
	_, body, _ = e.get(e.c, "/audit")
	if strings.Contains(body, "secret.action") {
		t.Fatal("other org entry in list")
	}
}

func itoa(i int64) string {
	return strings.TrimSpace(strings.Replace(time.Duration(i).String(), "ns", "", 1))
}

func TestLocaleParity(t *testing.T) {
	e := newEnv(t)
	if m := e.srv.Bundle().Missing(); len(m) > 0 {
		t.Fatalf("locale keys missing: %v", m)
	}
}

func TestTemplatesHaveNoMissingKeys(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	for _, p := range []string{"/", "/audit", "/settings"} {
		_, body, _ := e.get(e.c, p)
		if strings.Contains(body, "[") && regexp.MustCompile(`\[[a-z0-9]+\.[a-z0-9.]+\]`).MatchString(body) {
			t.Fatalf("%s shows a missing translation key", p)
		}
	}
	for _, lang := range []string{"id", "en"} {
		c := e.client()
		c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, body, _ := e.get(c, "/login")
		if regexp.MustCompile(`\[[a-z0-9]+\.[a-z0-9.]+\]`).MatchString(body) {
			t.Fatalf("login (%s) shows a missing key", lang)
		}
	}
}

func TestOpenRedirectBlocked(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}, "back": {"//evil.example"}})
	if resp.Header.Get("Location") != "/" {
		t.Fatal("open redirect")
	}
}

func TestClientIPIgnoresSpoofedForwardedFor(t *testing.T) {
	s := &Server{cfg: Config{TrustProxy: true}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the entry added by the edge", got)
	}
	s.cfg.TrustProxy = false
	if got := s.clientIP(r); got == "203.0.113.9" || got == "1.2.3.4" {
		t.Fatalf("header trusted without TrustProxy: %q", got)
	}
}

func TestNewSetupLinkRevokesOld(t *testing.T) {
	e := newEnv(t)
	old, err := SetupLink(e.st, e.ts.URL, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetupLink(e.st, e.ts.URL, e.clock); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := e.get(e.client(), strings.TrimPrefix(old, e.ts.URL)); code == 200 {
		t.Fatal("older setup link still works after a new one was made")
	}
}

func TestEnrolShowsQRAndRenewsSecret(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	link, err := SetupLink(e.st, e.ts.URL, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(link, e.ts.URL)
	if code, _, _ := e.post(c, path, url.Values{"email": {"admin@example.test"}, "password": {pw}, "password2": {pw}}, path); code != 303 {
		t.Fatalf("setup: %d", code)
	}
	_, body1, _ := e.get(c, "/2fa/enrol")
	if !regexp.MustCompile(`<img src="data:image/png;base64,(?:[A-Za-z0-9/=]|&#43;){100,}"`).MatchString(body1) {
		t.Fatal("enrol page has no QR image (data URL filtered?)")
	}
	if strings.Contains(body1, "ZgotmplZ") {
		t.Fatal("template replaced the QR URL")
	}
	sec := regexp.MustCompile(`class="secret">([A-Z2-7]+)<`)
	_, body2, _ := e.get(c, "/2fa/enrol")
	s1, s2 := sec.FindStringSubmatch(body1), sec.FindStringSubmatch(body2)
	if s1 == nil || s2 == nil || s1[1] == s2[1] {
		t.Fatal("secret not renewed on a new visit before 2FA is enabled")
	}
	old, _ := auth.TOTPCode(s1[1], e.clock)
	if _, b, _ := e.postWith(c, "/2fa/enrol", url.Values{"code": {old}}, body2); strings.Contains(b, "recovery codes") {
		t.Fatal("an earlier, replaced secret enabled 2FA")
	}
	cur, _ := auth.TOTPCode(s2[1], e.clock)
	if _, b, _ := e.postWith(c, "/2fa/enrol", url.Values{"code": {cur}}, body2); !strings.Contains(b, "recovery codes") {
		t.Fatal("current secret did not enable 2FA")
	}
}
