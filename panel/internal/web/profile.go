package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

// Accent colours: "teal" is the brand default; the others only change the highlight colour of the UI.
// Saved per account (setting accent:user:<id>) and per browser (cookie) like the theme.
var accents = []string{"teal", "blue", "violet", "orange", "rose"}

const cookieAccent = "np_accent"

func validAccent(a string) bool {
	for _, x := range accents {
		if x == a {
			return true
		}
	}
	return false
}

func (s *Server) accent(r *http.Request) string {
	if u := userOf(r); u != nil {
		if a := s.st.Setting(fmt.Sprintf("accent:user:%d", u.ID), ""); validAccent(a) {
			return a
		}
	}
	if c, err := r.Cookie(cookieAccent); err == nil && validAccent(c.Value) {
		return c.Value
	}
	return "teal"
}

type profileData struct {
	Email     string
	TOTP      bool
	CodesLeft int
	Accents   []string
	Accent    string
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	s.renderProfile(w, r, http.StatusOK, "")
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, code int, errKey string) {
	u := userOf(r)
	n, _ := s.st.RecoveryCodesLeft(u.ID)
	p := page{Title: s.tr.T(s.lang(r), "nav.profile"), Active: "profile", Flash: r.URL.Query().Get("saved"),
		Data: profileData{Email: u.Email, TOTP: u.TOTPEnabled, CodesLeft: n, Accents: accents, Accent: s.accent(r)}}
	if errKey != "" {
		p.Error = s.tr.T(s.lang(r), errKey)
		p.Flash = ""
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	s.render(w, r, "profile", p)
}

// profileAppearance saves theme and accent colour on the account and in the browser.
func (s *Server) profileAppearance(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	t, a := r.PostForm.Get("theme"), r.PostForm.Get("accent")
	if !themes[t] || !validAccent(a) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	year := 365 * 24 * 3600
	http.SetCookie(w, &http.Cookie{Name: cookieTheme, Value: t, Path: "/", MaxAge: year, Secure: s.cfg.SecureCookies, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: cookieAccent, Value: a, Path: "/", MaxAge: year, Secure: s.cfg.SecureCookies, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	if err := errors.Join(s.st.SetSetting(fmt.Sprintf("theme:user:%d", u.ID), t), s.st.SetSetting(fmt.Sprintf("accent:user:%d", u.ID), a)); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	http.Redirect(w, r, "/profile?saved=look", http.StatusSeeOther)
}

// profilePassword changes the password. It needs the current password; on success every other
// session of the account is signed out and this browser gets a fresh session.
func (s *Server) profilePassword(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
		s.renderProfile(w, r, http.StatusTooManyRequests, "err.toomany")
		return
	}
	cur, pw1, pw2 := r.PostForm.Get("current"), r.PostForm.Get("password"), r.PostForm.Get("password2")
	if !auth.CheckPassword(u.PwHash, cur) {
		s.audit1(u, r, "account.password_change_failed", "wrong current password")
		s.renderProfile(w, r, http.StatusUnprocessableEntity, "err.pwcurrent")
		return
	}
	switch {
	case pw1 != pw2:
		s.renderProfile(w, r, http.StatusUnprocessableEntity, "err.pwmatch")
		return
	case pw1 == cur:
		s.renderProfile(w, r, http.StatusUnprocessableEntity, "err.pwsame")
		return
	}
	hash, err := auth.HashPassword(pw1)
	if err != nil {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, "err.pwshort")
		return
	}
	if err := s.st.SetPassword(u.ID, hash); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	_ = s.st.DeleteUserSessions(u.ID)
	if _, err := s.startSession(w, u, true); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s.audit1(u, r, "account.password_changed", "other sessions signed out")
	http.Redirect(w, r, "/profile?saved=pw", http.StatusSeeOther)
}
