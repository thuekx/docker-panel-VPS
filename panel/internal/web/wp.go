package web

// WordPress Tools page (WP-01). Every action goes to nostek-agent, which runs WP-CLI in the site's own cli
// container. The one-click login link is shown to the browser only: it is never logged or stored by the panel.

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

var (
	wpTabs    = []string{"dashboard", "plugins", "themes", "cli"}
	wpItemRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	wpSlugRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
	wpSetKeys = map[string]bool{"indexing": true, "maintenance": true, "debug": true, "system_cron": true}
	wpItemDos = map[string]bool{"activate": true, "deactivate": true, "update": true, "delete": true, "install": true, "auto-on": true, "auto-off": true}
)

type wpData struct {
	Site      agent.Site
	WP        agent.WP
	Err       string
	Tab       string
	Output    string // report of the last change
	OutputErr string
	Check     *agent.Check
	Line      string // console line
	CLI       *agent.CLIResult
	TLSUntil  string
	TLSDays   int
}

func wpTab(t string) string {
	for _, x := range wpTabs {
		if t == x {
			return t
		}
	}
	return "dashboard"
}

func (s *Server) wpPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	s.renderWP(w, r, site, wpData{Tab: wpTab(r.URL.Query().Get("tab"))}, http.StatusOK)
}

func (s *Server) renderWP(w http.ResponseWriter, r *http.Request, site agent.Site, d wpData, code int) {
	long(w, 2*time.Minute)
	ctx, cancel := s.agentCtx(r, 100*time.Second)
	defer cancel()
	d.Site = site
	x, err := s.ag.WordPress(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.WP = x
	if x.TLS.Expires > 0 {
		t := time.Unix(x.TLS.Expires, 0)
		d.TLSUntil = t.In(s.loc()).Format("02 Jan 2006")
		d.TLSDays = int(time.Until(t).Hours() / 24)
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	s.render(w, r, "wp", page{Title: "WordPress · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// wpAction handles every form of the WordPress page.
func (s *Server) wpAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f := r.PostForm
	u := userOf(r)
	back := func(tab, done string, err error) {
		q := "?tab=" + tab
		if err != nil {
			q += "&err=" + url.QueryEscape(err.Error())
		} else if done != "" {
			q += "&done=" + done
		}
		http.Redirect(w, r, "/wp/"+site.ID+q, http.StatusSeeOther)
	}
	switch op := f.Get("op"); op {
	case "login":
		ctx, cancel := s.agentCtx(r, 70*time.Second)
		defer cancel()
		long(w, 2*time.Minute)
		link, user, err := s.ag.Login(ctx, site.ID)
		if err != nil {
			s.audit1(u, r, "wp.login", site.ID+" failed: "+err.Error())
			back("dashboard", "", err)
			return
		}
		s.audit1(u, r, "wp.login", site.ID+" as "+user)
		s.render(w, r, "wplogin", page{Title: "wp-admin · " + site.Domain, Active: "sites",
			Data: map[string]string{"URL": link, "User": user, "Site": site.ID, "Domain": site.Domain}})
	case "set":
		key, on := f.Get("key"), f.Get("on") == "1"
		if !wpSetKeys[key] {
			http.Error(w, "bad request", 400)
			return
		}
		ctx, cancel := s.agentCtx(r, 3*time.Minute)
		defer cancel()
		long(w, 4*time.Minute)
		err := s.ag.SetWP(ctx, site.ID, key, on)
		s.audit1(u, r, "wp.set", fmt.Sprintf("%s %s=%v%s", site.ID, key, on, failed(err)))
		back("dashboard", "set", err)
	case "core", "plugin", "theme":
		tab := "dashboard"
		ctx, cancel := s.agentCtx(r, 16*time.Minute)
		defer cancel()
		long(w, 17*time.Minute)
		var out, what string
		var err error
		if op == "core" {
			do := f.Get("do")
			if do != "update" && do != "all" {
				http.Error(w, "bad request", 400)
				return
			}
			what = do
			out, err = s.ag.Core(ctx, site.ID, do)
		} else {
			tab = op + "s"
			a := agent.ItemAction{Do: f.Get("do"), All: f.Get("all") == "1", Activate: f.Get("activate") == "1"}
			re := wpItemRe
			if a.Do == "install" {
				re = wpSlugRe
			}
			for _, n := range f["name"] {
				n = strings.TrimSpace(n)
				if !re.MatchString(n) {
					s.renderWP(w, r, site, wpData{Tab: tab, OutputErr: s.tr.T(s.lang(r), "wp.err.name")}, http.StatusUnprocessableEntity)
					return
				}
				a.Names = append(a.Names, n)
			}
			if !wpItemDos[a.Do] || len(a.Names) > 50 {
				http.Error(w, "bad request", 400)
				return
			}
			what = a.Do + " " + strings.Join(a.Names, ",")
			if a.All {
				what = a.Do + " all"
			}
			out, err = s.ag.Items(ctx, site.ID, op+"s", a)
		}
		s.audit1(u, r, "wp."+op, site.ID+" "+what+failed(err))
		d := wpData{Tab: tab, Output: out}
		code := http.StatusOK
		if err != nil {
			d.Output, d.OutputErr, code = "", err.Error(), http.StatusUnprocessableEntity
		}
		s.renderWP(w, r, site, d, code)
	case "check":
		ctx, cancel := s.agentCtx(r, 4*time.Minute)
		defer cancel()
		long(w, 5*time.Minute)
		c, err := s.ag.Integrity(ctx, site.ID)
		s.audit1(u, r, "wp.check", fmt.Sprintf("%s core=%v plugins=%v%s", site.ID, c.CoreOK, c.PluginsOK, failed(err)))
		d := wpData{Tab: "dashboard", Check: &c}
		code := http.StatusOK
		if err != nil {
			d.Check, d.OutputErr, code = nil, err.Error(), http.StatusUnprocessableEntity
		}
		s.renderWP(w, r, site, d, code)
	case "cli":
		line := strings.TrimSpace(f.Get("line"))
		ctx, cancel := s.agentCtx(r, 4*time.Minute)
		defer cancel()
		long(w, 5*time.Minute)
		res, err := s.ag.CLI(ctx, site.ID, line)
		// only the command name goes to the audit log: arguments may hold a password
		first := "?"
		if fl := strings.Fields(strings.TrimPrefix(line, "wp ")); len(fl) > 0 && cmdName.MatchString(fl[0]) {
			first = fl[0]
		}
		s.audit1(u, r, "wp.cli", fmt.Sprintf("%s %s (%d chars)%s", site.ID, first, len(line), failed(err)))
		d := wpData{Tab: "cli", Line: line, CLI: &res}
		code := http.StatusOK
		if err != nil {
			d.CLI, d.OutputErr, code = nil, err.Error(), http.StatusUnprocessableEntity
		}
		s.renderWP(w, r, site, d, code)
	default:
		http.Error(w, "bad request", 400)
	}
}

var cmdName = regexp.MustCompile(`^[a-z][a-z-]{0,19}$`)

func failed(err error) string {
	if err == nil {
		return ""
	}
	return " failed: " + err.Error()
}
