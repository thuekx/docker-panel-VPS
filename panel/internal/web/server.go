// Package web serves the panel UI.
package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/i18n"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
	"github.com/thuekx/docker-panel-VPS/panel/internal/sysinfo"
)

const (
	cookieSession = "np_session"
	cookieLang    = "np_lang"
	cookieTheme   = "np_theme"
	cookiePre     = "np_pre" // CSRF for forms shown before login
	idleTimeout   = 12 * time.Hour
	maxFails      = 10
	lockFor       = 15 * time.Minute
)

type Config struct {
	SecureCookies bool
	TrustProxy    bool
	DefaultLang   string
	Issuer        string
	Version       string
}

type Server struct {
	cfg     Config
	st      *store.Store
	tr      *i18n.Bundle
	tpl     map[string]*template.Template
	static  http.Handler
	sys     sysinfo.Reader
	ipLimit *auth.Limiter
	now     func() time.Time
	ag      *agent.Client
	owners  jobOwners
}

func New(cfg Config, st *store.Store, assets fs.FS, sys sysinfo.Reader) (*Server, error) {
	if cfg.DefaultLang == "" {
		cfg.DefaultLang = "id"
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "Nostek Panel"
	}
	tr, err := i18n.Load(assets, "locales", cfg.DefaultLang)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, st: st, tr: tr, sys: sys, ipLimit: auth.NewLimiter(20, time.Minute), now: time.Now, ag: agent.New("")}
	if err := s.parseTemplates(assets); err != nil {
		return nil, err
	}
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return s, nil
}

// Bundle exposes the translations (used by the parity test).
func (s *Server) Bundle() *i18n.Bundle { return s.tr }

func (s *Server) parseTemplates(assets fs.FS) error {
	s.tpl = map[string]*template.Template{}
	pages := []string{"login", "login2fa", "setup", "enrol", "codes", "dashboard", "audit", "settings", "reveal", "message", "sites", "sitenew", "sitejob", "site", "profile", "files", "fileedit", "db", "wp", "wplogin"}
	for _, p := range pages {
		t := template.New("layout.html").Funcs(template.FuncMap{
			"t": func(lang, key string, args ...any) string { return s.tr.T(lang, key, args...) },
			"pct": func(a, b float64) int {
				if b <= 0 {
					return 0
				}
				return int(a / b * 100)
			},
			"pcti": func(a, b int64) int {
				if b <= 0 {
					return 0
				}
				return int(a * 100 / b)
			},
			"f1":    func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
			"list3": func(a, b, c string) []string { return []string{a, b, c} },
			"add3": func(a, b, c int) int { return a + b + c },
			"len1": func(s string) int {
				if s == "" {
					return 0
				}
				return 1
			},
			"dict": func(kv ...any) map[string]any {
				m := map[string]any{}
				for i := 0; i+1 < len(kv); i += 2 {
					if k, ok := kv[i].(string); ok {
						m[k] = kv[i+1]
					}
				}
				return m
			},
			"dict3": func(lang, st, phase string) map[string]string {
				return map[string]string{"L": lang, "S": st, "P": phase}
			},
			"dict2": func(st sysinfo.Status, label string) map[string]any {
				return map[string]any{"S": string(st), "L": label}
			},
		})
		t, err := t.ParseFS(assets, "templates/layout.html", "templates/"+p+".html")
		if err != nil {
			return err
		}
		s.tpl[p] = t
	}
	return nil
}

// Handler returns the full HTTP handler with security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /setup/{token}", s.setupForm)
	mux.HandleFunc("POST /setup/{token}", s.setupSubmit)
	mux.HandleFunc("GET /reveal/{token}", s.revealForm)
	mux.HandleFunc("POST /reveal/{token}", s.revealSubmit)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("GET /login/2fa", s.need(false, s.login2faForm))
	mux.HandleFunc("POST /login/2fa", s.need(false, s.login2faSubmit))
	mux.HandleFunc("GET /2fa/enrol", s.need(false, s.enrolForm))
	mux.HandleFunc("POST /2fa/enrol", s.need(false, s.enrolSubmit))
	mux.HandleFunc("POST /logout", s.need(false, s.logout))
	mux.HandleFunc("POST /lang", s.setLang)
	mux.HandleFunc("POST /theme", s.setTheme)
	mux.HandleFunc("GET /{$}", s.need(true, s.dashboard))
	mux.HandleFunc("GET /audit", s.need(true, s.audit))
	mux.HandleFunc("GET /audit/{id}", s.need(true, s.auditEntry))
	mux.HandleFunc("GET /sites", s.need(true, s.sitesList))
	mux.HandleFunc("GET /sites/new", s.need(true, s.siteForm))
	mux.HandleFunc("POST /sites/new", s.need(true, s.siteSubmit))
	mux.HandleFunc("GET /sites/job/{job}", s.need(true, s.siteJob))
	mux.HandleFunc("POST /sites/job/{job}/secret", s.need(true, s.siteSecret))
	mux.HandleFunc("GET /sites/{id}", s.need(true, s.siteShow))
	mux.HandleFunc("GET /stack/{id}", s.need(true, s.siteStack))
	mux.HandleFunc("POST /sites/{id}", s.need(true, s.siteAction))
	mux.HandleFunc("POST /trash/{entry}/restore", s.need(true, s.siteRestore))
	mux.HandleFunc("GET /sites/job/{job}/status", s.need(true, s.siteJobStatus))
	mux.HandleFunc("GET /files/{id}", s.need(true, s.filesPage))
	mux.HandleFunc("POST /files/{id}", s.need(true, s.filesAction))
	mux.HandleFunc("GET /files/{id}/edit", s.need(true, s.filesEdit))
	mux.HandleFunc("GET /files/{id}/download", s.need(true, s.filesDownload(false)))
	mux.HandleFunc("GET /files/{id}/zip", s.need(true, s.filesDownload(true)))
	mux.HandleFunc("POST /files/{id}/upload", s.needMultipart(s.filesUpload))
	mux.HandleFunc("GET /wp/{id}", s.need(true, s.wpPage))
	mux.HandleFunc("POST /wp/{id}", s.need(true, s.wpAction))
	mux.HandleFunc("GET /db/{id}", s.need(true, s.dbPage))
	mux.HandleFunc("POST /db/{id}", s.need(true, s.dbAction))
	mux.HandleFunc("GET /db/{id}/backups/{name}", s.need(true, s.dbBackup))
	mux.HandleFunc("POST /db/{id}/import", s.needMultipart(s.dbImport))
	mux.HandleFunc("GET /profile", s.need(true, s.profile))
	mux.HandleFunc("POST /profile/appearance", s.need(true, s.profileAppearance))
	mux.HandleFunc("POST /profile/password", s.need(true, s.profilePassword))
	mux.HandleFunc("GET /settings", s.need(true, s.settings))
	mux.HandleFunc("POST /settings", s.need(true, s.settingsSubmit))
	mux.HandleFunc("POST /settings/recovery", s.need(true, s.newRecoveryCodes))
	return s.headers(mux)
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- request context

type ctxKey int

const (
	keyUser ctxKey = iota
	keySess
)

func userOf(r *http.Request) *store.User { u, _ := r.Context().Value(keyUser).(*store.User); return u }
func sessOf(r *http.Request) *store.Session {
	s, _ := r.Context().Value(keySess).(*store.Session)
	return s
}

// need wraps a handler that requires a session; full=true also requires finished 2FA.
func (s *Server) need(full bool, h http.HandlerFunc) http.HandlerFunc {
	return s.needWith(full, true, h)
}

// needMultipart is need(true) for streamed multipart uploads: the handler checks the CSRF token itself, from the
// form's first part, so the body is never parsed into memory or onto disk first.
func (s *Server) needMultipart(h http.HandlerFunc) http.HandlerFunc {
	return s.needWith(true, false, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Error(w, "bad request", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

func (s *Server) needWith(full, csrf bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, u := s.currentSession(r)
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if csrf && r.Method == http.MethodPost && !s.csrfOK(r, sess.CSRF) {
			http.Error(w, "bad request", http.StatusForbidden)
			return
		}
		if full && !sess.MFAOK {
			if u.TOTPEnabled {
				http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
			} else {
				http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
			}
			return
		}
		_ = s.st.TouchSession(sess.ID, s.now())
		ctx := context.WithValue(context.WithValue(r.Context(), keyUser, u), keySess, sess)
		h(w, r.WithContext(ctx))
	}
}

func (s *Server) currentSession(r *http.Request) (*store.Session, *store.User) {
	c, err := r.Cookie(cookieSession)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	sess, err := s.st.SessionByID(auth.TokenHash(c.Value))
	if err != nil {
		return nil, nil
	}
	if s.now().Sub(sess.LastSeen) > idleTimeout {
		_ = s.st.DeleteSession(sess.ID)
		return nil, nil
	}
	u, err := s.st.UserByID(sess.UserID)
	if err != nil {
		return nil, nil
	}
	return sess, u
}

func (s *Server) startSession(w http.ResponseWriter, u *store.User, mfa bool) (*store.Session, error) {
	tok := auth.NewToken()
	sess := &store.Session{ID: auth.TokenHash(tok), UserID: u.ID, CSRF: auth.NewToken(), MFAOK: mfa, CreatedAt: s.now(), LastSeen: s.now()}
	if err := s.st.CreateSession(sess); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: tok, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	return sess, nil
}

// upgradeSession replaces a pre-2FA session with a fresh full session (prevents fixation).
func (s *Server) upgradeSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if old := sessOf(r); old != nil {
		_ = s.st.DeleteSession(old.ID)
	}
	_, err := s.startSession(w, u, true)
	return err
}

func (s *Server) csrfOK(r *http.Request, want string) bool {
	if err := r.ParseForm(); err != nil {
		return false
	}
	got := r.PostForm.Get("csrf")
	return got != "" && want != "" && subtleEq(got, want)
}

// preCSRF issues a double-submit token for forms shown before login.
func (s *Server) preCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookiePre); err == nil && len(c.Value) > 20 {
		return c.Value
	}
	tok := auth.NewToken()
	http.SetCookie(w, &http.Cookie{Name: cookiePre, Value: tok, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	return tok
}

func (s *Server) preCSRFOK(r *http.Request) bool {
	c, err := r.Cookie(cookiePre)
	if err != nil {
		return false
	}
	return s.csrfOK(r, c.Value)
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			// the right-most entry is the one our edge proxy added; earlier ones come from the client
			parts := strings.Split(xf, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) lang(r *http.Request) string {
	if c, err := r.Cookie(cookieLang); err == nil && s.tr.Has(c.Value) {
		return c.Value
	}
	return s.cfg.DefaultLang
}

// Themes: "system" follows the device; "light" and "dark" are fixed. Saved per account when signed in
// (setting theme:user:<id>), otherwise per browser (cookie).
var themes = map[string]bool{"system": true, "light": true, "dark": true}

func (s *Server) theme(r *http.Request) string {
	if u := userOf(r); u != nil {
		if t := s.st.Setting(fmt.Sprintf("theme:user:%d", u.ID), ""); themes[t] {
			return t
		}
	}
	if c, err := r.Cookie(cookieTheme); err == nil && themes[c.Value] {
		return c.Value
	}
	return "system"
}

func safeBack(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/setup/") || strings.HasPrefix(p, "/reveal/") {
		return "/"
	}
	return p
}

func (s *Server) loc() *time.Location {
	l, err := time.LoadLocation(s.st.Setting("timezone", "Asia/Jakarta"))
	if err != nil {
		l, _ = time.LoadLocation("Asia/Jakarta")
	}
	return l
}

// ---------- rendering

type page struct {
	Lang, Theme, Accent, Back         string
	Title, CSRF, Error, Flash, Active string
	User                              *store.User
	Version                           string
	Now                               string
	Data                              any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.Lang = s.lang(r)
	p.Theme = s.theme(r)
	p.Accent = s.accent(r)
	p.Back = safeBack(r.URL.Path)
	p.Version = s.cfg.Version
	p.Now = s.now().In(s.loc()).Format("02 Jan 2006 15:04 MST")
	if p.User == nil {
		p.User = userOf(r)
	}
	if p.CSRF == "" {
		if sess := sessOf(r); sess != nil {
			p.CSRF = sess.CSRF
		}
	}
	var buf bytes.Buffer
	if err := s.tpl[name].Execute(&buf, p); err != nil {
		log.Printf("template %s: %v", name, err)
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, code int, key string) {
	w.WriteHeader(code)
	s.render(w, r, "message", page{Title: s.tr.T(s.lang(r), key+".title"), Data: key})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) { s.message(w, r, 404, "notfound") }

func (s *Server) audit1(u *store.User, r *http.Request, action, detail string) {
	if u == nil {
		return
	}
	_ = s.st.Audit(u.OrgID, u.ID, u.Email, action, detail, s.clientIP(r))
}

// ---------- setup (first admin) via one-time link

func (s *Server) setupForm(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.st.PeekToken(auth.TokenHash(r.PathValue("token")), "setup", s.now()); err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	s.render(w, r, "setup", page{Title: s.tr.T(s.lang(r), "setup.title"), CSRF: s.preCSRF(w, r)})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !s.preCSRFOK(r) {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	if _, _, err := s.st.PeekToken(auth.TokenHash(tok), "setup", s.now()); err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	lang := s.lang(r)
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	pw, pw2 := r.PostForm.Get("password"), r.PostForm.Get("password2")
	fail := func(key string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "setup", page{Title: s.tr.T(lang, "setup.title"), CSRF: s.preCSRF(w, r), Error: s.tr.T(lang, key)})
	}
	switch {
	case !strings.Contains(email, "@") || len(email) > 200:
		fail("err.email")
		return
	case pw != pw2:
		fail("err.pwmatch")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		fail("err.pwshort")
		return
	}
	if err := s.st.UseToken(auth.TokenHash(tok), "setup", s.now()); err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	u, err := s.st.CreateFirstAdmin(s.st.Setting("org_name", "Nostek"), email, hash)
	if err != nil {
		s.message(w, r, 409, "linkgone")
		return
	}
	s.audit1(u, r, "admin.created", "first admin via setup link")
	if _, err := s.startSession(w, u, false); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
}

// ---------- login

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if sess, _ := s.currentSession(r); sess != nil && sess.MFAOK {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login", page{Title: s.tr.T(s.lang(r), "login.title"), CSRF: s.preCSRF(w, r)})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	lang := s.lang(r)
	if !s.preCSRFOK(r) {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	fail := func(code int, key string) {
		w.WriteHeader(code)
		s.render(w, r, "login", page{Title: s.tr.T(lang, "login.title"), CSRF: s.preCSRF(w, r), Error: s.tr.T(lang, key)})
	}
	if !s.ipLimit.Allow("login:" + s.clientIP(r)) {
		fail(http.StatusTooManyRequests, "err.toomany")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	pw := r.PostForm.Get("password")
	u, err := s.st.UserByEmail(email)
	if err != nil {
		auth.DummyCheck(pw)
		fail(http.StatusUnauthorized, "err.login")
		return
	}
	if u.LockedUntil.After(s.now()) {
		auth.DummyCheck(pw)
		s.audit1(u, r, "login.locked", "")
		fail(http.StatusUnauthorized, "err.login")
		return
	}
	if !auth.CheckPassword(u.PwHash, pw) {
		_ = s.st.RecordLoginFailure(u.ID, maxFails, s.now().Add(lockFor))
		s.audit1(u, r, "login.failed", "wrong password")
		fail(http.StatusUnauthorized, "err.login")
		return
	}
	_ = s.st.ResetLoginFailures(u.ID)
	if _, err := s.startSession(w, u, false); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if u.TOTPEnabled {
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
	} else {
		http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
	}
}

func (s *Server) login2faForm(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if sessOf(r).MFAOK {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !u.TOTPEnabled {
		http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
		return
	}
	s.render(w, r, "login2fa", page{Title: s.tr.T(s.lang(r), "login2fa.title")})
}

func (s *Server) login2faSubmit(w http.ResponseWriter, r *http.Request) {
	u, lang := userOf(r), s.lang(r)
	if !u.TOTPEnabled {
		http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
		return
	}
	fail := func(code int, key string) {
		w.WriteHeader(code)
		s.render(w, r, "login2fa", page{Title: s.tr.T(lang, "login2fa.title"), Error: s.tr.T(lang, key)})
	}
	if !s.ipLimit.Allow("2fa:" + s.clientIP(r)) {
		fail(http.StatusTooManyRequests, "err.toomany")
		return
	}
	code := r.PostForm.Get("code")
	ok := false
	if step, valid := auth.VerifyTOTP(u.TOTPSecret, code, s.now()); valid {
		ok, _ = s.st.UseTOTPStep(u.ID, step) // refuses replay of the same code
	} else if rc := auth.NormalizeRecovery(code); len(rc) == 14 {
		ok, _ = s.st.UseRecoveryCode(u.ID, auth.TokenHash(rc))
		if ok {
			s.audit1(u, r, "login.recovery_code", "")
		}
	}
	if !ok {
		_ = s.st.RecordLoginFailure(u.ID, maxFails, s.now().Add(lockFor))
		s.audit1(u, r, "login.2fa_failed", "")
		fail(http.StatusUnauthorized, "err.code")
		return
	}
	if err := s.upgradeSession(w, r, u); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(u, r, "login.ok", "")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------- 2FA enrolment

func (s *Server) enrolForm(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if u.TOTPEnabled {
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}
	// A fresh secret on every visit until 2FA is enabled: a key that was shown before (and may have been
	// seen or shared) never becomes the active one unless it is confirmed with a code right away.
	u.TOTPSecret = auth.NewTOTPSecret()
	if err := s.st.SetTOTPSecret(u.ID, u.TOTPSecret); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.render(w, r, "enrol", page{Title: s.tr.T(s.lang(r), "enrol.title"), Data: s.enrolData(u)})
}

// QR is a template.URL: html/template replaces a plain-string data: URL in src with "#ZgotmplZ".
type enrolData struct {
	QR     template.URL
	Secret string
}

func (s *Server) enrolData(u *store.User) enrolData {
	uri := auth.OTPAuthURL(s.cfg.Issuer, u.Email, u.TOTPSecret)
	png, err := qrcode.Encode(uri, qrcode.Medium, 240)
	var qr template.URL
	if err == nil {
		qr = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)) // built here from our own PNG bytes only
	}
	return enrolData{QR: qr, Secret: u.TOTPSecret}
}

func (s *Server) enrolSubmit(w http.ResponseWriter, r *http.Request) {
	u, lang := userOf(r), s.lang(r)
	if u.TOTPEnabled || u.TOTPSecret == "" {
		http.Redirect(w, r, "/2fa/enrol", http.StatusSeeOther)
		return
	}
	step, ok := auth.VerifyTOTP(u.TOTPSecret, r.PostForm.Get("code"), s.now())
	if !ok {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "enrol", page{Title: s.tr.T(lang, "enrol.title"), Error: s.tr.T(lang, "err.code"), Data: s.enrolData(u)})
		return
	}
	if err := s.st.EnableTOTP(u.ID, step); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	codes, err := s.issueRecoveryCodes(u.ID)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if err := s.upgradeSession(w, r, u); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(u, r, "2fa.enabled", "")
	s.render(w, r, "codes", page{Title: s.tr.T(lang, "codes.title"), Data: codes})
}

func (s *Server) issueRecoveryCodes(userID int64) ([]string, error) {
	codes := auth.RecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.TokenHash(c)
	}
	return codes, s.st.ReplaceRecoveryCodes(userID, hashes)
}

func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	codes, err := s.issueRecoveryCodes(u.ID)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(u, r, "2fa.recovery_codes_renewed", "")
	s.render(w, r, "codes", page{Title: s.tr.T(s.lang(r), "codes.title"), Data: codes})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess := sessOf(r); sess != nil {
		_ = s.st.DeleteSession(sess.ID)
	}
	s.audit1(userOf(r), r, "logout", "")
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) setLang(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	l := r.PostForm.Get("lang")
	if s.tr.Has(l) {
		http.SetCookie(w, &http.Cookie{Name: cookieLang, Value: l, Path: "/", MaxAge: 365 * 24 * 3600, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	}
	back := r.PostForm.Get("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) setTheme(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	t := r.PostForm.Get("theme")
	if themes[t] {
		http.SetCookie(w, &http.Cookie{Name: cookieTheme, Value: t, Path: "/", MaxAge: 365 * 24 * 3600, Secure: s.cfg.SecureCookies, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		// signed in with finished 2FA and a valid form token: also keep it on the account
		if sess, u := s.currentSession(r); sess != nil && sess.MFAOK && s.csrfOK(r, sess.CSRF) {
			_ = s.st.SetSetting(fmt.Sprintf("theme:user:%d", u.ID), t)
		}
	}
	http.Redirect(w, r, safeBack(r.PostForm.Get("back")), http.StatusSeeOther)
}

// ---------- pages

type dashData struct {
	S        sysinfo.Snapshot
	Backup   string
	TZ       string
	CodesLow bool
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	snap := s.sys.Read(r.Context())
	d := dashData{S: snap, TZ: s.loc().String()}
	if !snap.BackupLast.IsZero() {
		d.Backup = snap.BackupLast.In(s.loc()).Format("02 Jan 2006 15:04 MST")
	}
	if n, _ := s.st.RecoveryCodesLeft(userOf(r).ID); n < 3 {
		d.CodesLow = true
	}
	s.render(w, r, "dashboard", page{Title: s.tr.T(s.lang(r), "nav.dashboard"), Active: "dashboard", Data: d})
}

type auditRow struct {
	ID                            int64
	At, Email, Action, Detail, IP string
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	list, err := s.st.AuditList(u.OrgID, 200)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	rows := make([]auditRow, 0, len(list))
	for _, a := range list {
		rows = append(rows, auditRow{a.ID, a.At.In(s.loc()).Format("02 Jan 2006 15:04:05 MST"), a.Email, a.Action, a.Detail, a.IP})
	}
	s.render(w, r, "audit", page{Title: s.tr.T(s.lang(r), "nav.audit"), Active: "audit", Data: rows})
}

// auditEntry shows one entry; an entry of another organisation answers 404 (never 403).
func (s *Server) auditEntry(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	a, err := s.st.AuditEntryFor(u.OrgID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	rows := []auditRow{{a.ID, a.At.In(s.loc()).Format("02 Jan 2006 15:04:05 MST"), a.Email, a.Action, a.Detail, a.IP}}
	s.render(w, r, "audit", page{Title: s.tr.T(s.lang(r), "nav.audit"), Active: "audit", Data: rows})
}

type settingsData struct {
	TZ        string
	Zones     []string
	CodesLeft int
}

var zones = []string{"Asia/Jakarta", "Asia/Makassar", "Asia/Jayapura", "Asia/Singapore", "UTC"}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	n, _ := s.st.RecoveryCodesLeft(userOf(r).ID)
	s.render(w, r, "settings", page{Title: s.tr.T(s.lang(r), "nav.settings"), Active: "settings",
		Data: settingsData{TZ: s.loc().String(), Zones: zones, CodesLeft: n}, Flash: r.URL.Query().Get("saved")})
}

func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request) {
	tz := r.PostForm.Get("timezone")
	valid := false
	for _, z := range zones {
		valid = valid || z == tz
	}
	if !valid {
		http.Error(w, "bad request", 400)
		return
	}
	old := s.loc().String()
	if err := s.st.SetSetting("timezone", tz); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(userOf(r), r, "settings.timezone", old+" -> "+tz)
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// ---------- break-glass password reveal (one-time link)

func (s *Server) revealForm(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.st.PeekToken(auth.TokenHash(r.PathValue("token")), "reveal", s.now()); err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	s.render(w, r, "reveal", page{Title: s.tr.T(s.lang(r), "reveal.title"), CSRF: s.preCSRF(w, r)})
}

// revealSubmit shows the new password once; the link is burned in the same step.
func (s *Server) revealSubmit(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !s.preCSRFOK(r) {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	uid, sealed, err := s.st.PeekToken(auth.TokenHash(tok), "reveal", s.now())
	if err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	pw, err := auth.Open(tok, sealed)
	if err != nil || s.st.UseToken(auth.TokenHash(tok), "reveal", s.now()) != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	if u, err := s.st.UserByID(uid); err == nil {
		s.audit1(u, r, "admin.password_revealed", "one-time link used")
	}
	s.render(w, r, "reveal", page{Title: s.tr.T(s.lang(r), "reveal.title"), Data: string(pw)})
}

func subtleEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
