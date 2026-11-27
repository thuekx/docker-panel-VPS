package web

// EXT-01: extension points (package panel/ext). Users that are not admins may use only what an extension allows;
// with no extension they can do nothing. The checks here run in needWith, after sign-in and two-factor login.

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/ext"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
	"github.com/thuekx/docker-panel-VPS/panel/internal/store"
)

var (
	extNameRe = regexp.MustCompile(`^[a-z0-9]{2,16}$`)
	emailRe   = regexp.MustCompile(`^[a-z0-9._%+-]{1,64}@[a-z0-9.-]{1,189}\.[a-z]{2,24}$`)
)

// accountPages are the pages every signed-in user may open (their own profile, help, signing out).
var accountPages = []string{
	"GET /{$}", "GET /login/2fa", "POST /login/2fa", "GET /2fa/enrol", "POST /2fa/enrol", "POST /logout",
	"GET /profile", "POST /profile/appearance", "POST /profile/password", "POST /profile/passkeys/begin",
	"POST /profile/passkeys/finish", "POST /profile/passkeys/delete",
	"GET /help", "GET /help/changelog", "GET /help/{slug}", "GET /sites",
}

// jobPages are a job's pages: a user that is not an admin sees only the jobs it started.
var jobPages = []string{"GET /sites/job/{job}", "GET /sites/job/{job}/status", "POST /sites/job/{job}/secret",
	"POST /sites/job/{job}/setup-lock"}

// SetExtensions installs the extension points; call it once, before Handler.
func (s *Server) SetExtensions(e ext.Extensions) error {
	if e.Name != "" && !extNameRe.MatchString(e.Name) {
		return fmt.Errorf("extension name %q: want 2-16 of a-z0-9", e.Name)
	}
	if e.Name == "" && len(e.Routes) > 0 {
		return errors.New("extension routes need a name")
	}
	s.ext = e
	if e.Start != nil {
		if err := e.Start(extHost{s}); err != nil {
			return fmt.Errorf("extension %s: %w", e.Name, err)
		}
	}
	s.extMux = http.NewServeMux()
	for _, rt := range s.ext.Routes {
		method, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok || (method != http.MethodGet && method != http.MethodPost) || !strings.HasPrefix(path, "/") {
			return fmt.Errorf("extension route %q: want \"GET /path\" or \"POST /path\"", rt.Pattern)
		}
		s.extMux.HandleFunc(method+" /x/"+e.Name+path, rt.Handler)
	}
	return nil
}

// extServe serves the extension's pages; sign-in, two-factor login and CSRF were checked by need.
func (s *Server) extServe(w http.ResponseWriter, r *http.Request) {
	if s.extMux == nil {
		s.notFound(w, r)
		return
	}
	h, pat := s.extMux.Handler(r)
	if pat == "" {
		s.notFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

func extUser(u *store.User) ext.User {
	return ext.User{ID: u.ID, OrgID: u.OrgID, Email: u.Email, Role: u.Role}
}

// mayUse decides whether u, who is not an admin, may make request r. Everything not listed is refused.
func (s *Server) mayUse(r *http.Request, u *store.User) bool {
	pat := r.Pattern
	switch {
	case slices.Contains(accountPages, pat):
		return true
	case pat == "/x/":
		return s.ext.Name != "" // the extension's handlers decide who may use them
	case slices.Contains(jobPages, pat):
		owner, ok := s.owners.owner(r.PathValue("job"))
		return ok && owner == u.ID
	case pat == "GET /sites/new" || pat == "POST /sites/new":
		return s.ext.CheckCreate != nil
	}
	id := r.PathValue("id")
	if !strings.Contains(pat, "{id}") || strings.HasPrefix(pat, "GET /audit/") || !siteRe.MatchString(id) {
		return false
	}
	action := ""
	if r.Method == http.MethodPost && r.PostForm != nil {
		if action = r.PostForm.Get("op"); action == "" {
			action = r.PostForm.Get("action")
		}
	}
	if acts, listed := ext.AdminOnly[pat]; listed && (acts == nil || slices.Contains(acts, action)) {
		return false
	}
	return s.siteAllowed(u, ext.Request{Site: id, Page: pat, Action: action})
}

func (s *Server) siteAllowed(u *store.User, req ext.Request) bool {
	if u.Role == ext.RoleAdmin {
		return true
	}
	return s.ext.Access != nil && s.ext.Access(extUser(u), req)
}

// visibleSites keeps the websites u may open.
func visibleSites[T any](s *Server, u *store.User, list []T, id func(T) string) []T {
	if u == nil || u.Role == ext.RoleAdmin {
		return list
	}
	var out []T
	for _, x := range list {
		if s.siteAllowed(u, ext.Request{Site: id(x), Page: "GET /sites/{id}"}) {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) onSite(ev ext.Event, u *store.User, site string) {
	if s.ext.OnSite != nil && u != nil {
		s.ext.OnSite(ev, extUser(u), site)
	}
}

// checkCreate asks the extension whether u may make the website (admins always may).
func (s *Server) checkCreate(u *store.User, domain, app, engine string) error {
	if u == nil || u.Role == ext.RoleAdmin {
		return nil
	}
	if s.ext.CheckCreate == nil {
		return errors.New("not allowed")
	}
	if engine == "" {
		engine = "docker"
	}
	return s.ext.CheckCreate(extUser(u), ext.Create{Domain: domain, App: app, Engine: engine})
}

// menu is the extension's menu for u.
func (s *Server) menu(u *store.User) []ext.MenuItem {
	if s.ext.Menu == nil || u == nil {
		return nil
	}
	var out []ext.MenuItem
	for _, m := range s.ext.Menu(extUser(u)) {
		if strings.HasPrefix(m.Href, "/x/"+s.ext.Name+"/") && m.Label != "" {
			out = append(out, m)
		}
	}
	return out
}

// extHost is the panel as an extension sees it.
type extHost struct{ s *Server }

func (h extHost) User(r *http.Request) (ext.User, bool) {
	u := userOf(r)
	if u == nil {
		return ext.User{}, false
	}
	return extUser(u), true
}

func (h extHost) CSRF(r *http.Request) string {
	if sess := sessOf(r); sess != nil {
		return sess.CSRF
	}
	return ""
}

func (h extHost) Lang(r *http.Request) string { return h.s.lang(r) }

type extPage struct{ Body template.HTML }

func (h extHost) Page(w http.ResponseWriter, r *http.Request, title string, body template.HTML) {
	h.s.render(w, r, "ext", page{Title: title, Active: "ext", Data: extPage{Body: body}})
}

func (h extHost) Audit(r *http.Request, action, detail string) {
	if u := userOf(r); u != nil {
		h.s.audit1(u, r, h.s.ext.Name+"."+action, detail)
	}
}

func (h extHost) NewUser(r *http.Request, email, role string) (int64, string, error) {
	admin := userOf(r)
	if admin == nil || admin.Role != ext.RoleAdmin {
		return 0, "", errors.New("only an admin makes users")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRe.MatchString(email) || role == "" || role == ext.RoleAdmin {
		return 0, "", errors.New("bad e-mail or role")
	}
	if _, err := h.s.st.UserByEmail(email); err == nil {
		return 0, "", errors.New("this e-mail has a login already")
	}
	pw := auth.RandomPassword(20)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return 0, "", err
	}
	u, err := h.s.st.CreateUser(admin.OrgID, email, hash, role)
	if err != nil {
		return 0, "", err
	}
	tok := auth.NewToken()
	sealed, err := auth.Seal(tok, []byte(pw))
	if err != nil {
		return 0, "", err
	}
	if err := h.s.st.CreateToken(auth.TokenHash(tok), "reveal", u.ID, sealed, h.s.now().Add(15*time.Minute)); err != nil {
		return 0, "", err
	}
	h.s.audit1(admin, r, "user.create", email+" role "+role)
	return u.ID, "/reveal/" + tok, nil
}

func (h extHost) target(r *http.Request, id int64) (*store.User, *store.User, error) {
	admin := userOf(r)
	if admin == nil || admin.Role != ext.RoleAdmin {
		return nil, nil, errors.New("only an admin changes users")
	}
	u, err := h.s.st.UserByID(id)
	if err != nil || u.OrgID != admin.OrgID {
		return nil, nil, store.ErrNotFound
	}
	if u.Role == ext.RoleAdmin {
		return nil, nil, store.ErrAdmin
	}
	return admin, u, nil
}

func (h extHost) SetLocked(r *http.Request, id int64, locked bool) error {
	admin, u, err := h.target(r, id)
	if err != nil {
		return err
	}
	if err := h.s.st.SetUserLocked(u.ID, locked, h.s.now()); err != nil {
		return err
	}
	h.s.audit1(admin, r, map[bool]string{true: "user.lock", false: "user.unlock"}[locked], u.Email)
	return nil
}

func (h extHost) DeleteUser(r *http.Request, id int64) error {
	admin, u, err := h.target(r, id)
	if err != nil {
		return err
	}
	if err := h.s.st.DeleteUser(u.ID); err != nil {
		return err
	}
	h.s.audit1(admin, r, "user.delete", u.Email)
	return nil
}

func (h extHost) Sites(ctx context.Context) ([]ext.Site, error) {
	list, err := h.s.ag.Sites(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ext.Site, 0, len(list))
	for _, x := range list {
		out = append(out, ext.Site{ID: x.ID, Domain: x.Domain, App: x.AppName(), Status: x.Status})
	}
	return out, nil
}

func (h extHost) SiteAction(ctx context.Context, id, action string) error {
	if !siteRe.MatchString(id) || (action != "start" && action != "stop") {
		return errors.New("bad website or action")
	}
	return h.s.ag.Action(ctx, id, action)
}

func (h extHost) DeleteSite(ctx context.Context, id string) (string, error) {
	if !siteRe.MatchString(id) {
		return "", errors.New("bad website")
	}
	return h.s.ag.Delete(ctx, id)
}
