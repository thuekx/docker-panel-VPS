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
	Notes     []wpNote // parts of the overview that could not be read, explained at the bottom
	// WP-02: the safe update job shown at the top
	Job     *agent.Job
	Steps   []stepRow
	Percent int
	Backup  string // the before-update backup
	PutBack bool   // the update broke the website and it was put back
	JobKey  string // locale prefix of the job card: wp.safe (update) | wp.push (staging pushed to live)
	StepKey string // locale prefix of its steps: wp.step | wp.pstep
	// WP-02 wave 2: the staging copy of this website, or the live website of this staging copy
	Staging *agent.Site
	Live    *agent.Site
}

// wpNote is one part of the overview that could not be read: Part names it for people, Detail is for support.
type wpNote struct{ Part, Detail string }

func wpNotes(warnings []string) []wpNote {
	var out []wpNote
	for _, w := range warnings {
		part := "other"
		for _, p := range []string{"plugins", "themes", "core"} {
			if strings.HasPrefix(w, p+": ") {
				part = p
			}
		}
		out = append(out, wpNote{Part: part, Detail: w})
	}
	return out
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
	d := wpData{Tab: wpTab(r.URL.Query().Get("tab"))}
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		ctx, cancel := s.agentCtx(r, 10*time.Second)
		j, err := s.ag.Job(ctx, id)
		cancel()
		if err == nil && j.Site == site.ID && (j.Kind == "wp-update" || j.Kind == "push") {
			d.Job, d.JobKey, d.StepKey = &j, "wp.safe", "wp.step"
			if j.Kind == "push" {
				d.JobKey, d.StepKey = "wp.push", "wp.pstep"
			}
			d.Steps, d.Percent = jobProgress(j, site)
			for _, st := range j.Steps {
				if name, ok := strings.CutPrefix(st, "done "); ok && backupNameRe.MatchString(name) {
					d.Backup = name
				}
				if st == "restore" && j.Kind == "wp-update" {
					d.PutBack = true
				}
			}
		}
	}
	s.renderWP(w, r, site, d, http.StatusOK)
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
	d.Notes = wpNotes(x.Warnings)
	if site.StagingOf != "" {
		if l, err := s.ag.Site(ctx, site.StagingOf); err == nil {
			d.Live = &l
		}
	} else if list, err := s.ag.Sites(ctx); err == nil {
		for i := range list {
			if list[i].StagingOf == site.ID {
				d.Staging = &list[i]
			}
		}
	}
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
		if op != "core" {
			tab = op + "s"
		}
		// WP-02: an update runs as a job with a backup first, unless the form asks for a quick one (safe=0)
		if do := f.Get("do"); f.Get("safe") != "0" && (op == "core" && (do == "update" || do == "all") || op != "core" && do == "update") {
			a := agent.ItemAction{Do: do, All: f.Get("all") == "1"}
			for _, n := range f["name"] {
				if n = strings.TrimSpace(n); !wpItemRe.MatchString(n) {
					s.renderWP(w, r, site, wpData{Tab: tab, OutputErr: s.tr.T(s.lang(r), "wp.err.name")}, http.StatusUnprocessableEntity)
					return
				}
				a.Names = append(a.Names, n)
			}
			if op != "core" && (len(a.Names) > 50 || len(a.Names) == 0 && !a.All) {
				http.Error(w, "bad request", 400)
				return
			}
			what := do + " " + strings.Join(a.Names, ",")
			if a.All {
				what = do + " all"
			}
			ctx, cancel := s.agentCtx(r, 30*time.Second)
			defer cancel()
			job, err := s.ag.SafeUpdate(ctx, site.ID, op, a)
			if err != nil {
				s.audit1(u, r, "wp."+op, site.ID+" safe "+what+failed(err))
				back(tab, "", err)
				return
			}
			s.owners.set(job, u.ID)
			s.audit1(u, r, "wp."+op, site.ID+" safe "+strings.TrimSpace(what)+" job "+job)
			http.Redirect(w, r, "/wp/"+site.ID+"?tab="+tab+"&job="+job+"#job", http.StatusSeeOther)
			return
		}
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
	case "staging": // WP-02 wave 2: make the staging copy
		if site.StagingOf != "" {
			http.Error(w, "bad request", 400)
			return
		}
		ctx, cancel := s.agentCtx(r, 30*time.Second)
		defer cancel()
		job, st, err := s.ag.StartStaging(ctx, site.ID)
		if err != nil {
			s.audit1(u, r, "wp.staging", site.ID+failed(err))
			back("dashboard", "", err)
			return
		}
		s.owners.set(job, u.ID)
		s.audit1(u, r, "wp.staging", site.ID+" -> "+st.ID+" job "+job)
		http.Redirect(w, r, "/sites/job/"+job, http.StatusSeeOther)
	case "push": // WP-02 wave 2: the staging copy over the live website, from either website's page
		live, from := site.ID, f.Get("from")
		if site.StagingOf != "" {
			live, from = site.StagingOf, site.ID
		}
		if f.Get("confirm") != "1" || !siteRe.MatchString(from) {
			http.Error(w, "bad request", 400)
			return
		}
		ctx, cancel := s.agentCtx(r, 30*time.Second)
		defer cancel()
		job, err := s.ag.PushStaging(ctx, live, from)
		if err != nil {
			s.audit1(u, r, "wp.push", from+" -> "+live+failed(err))
			back("dashboard", "", err)
			return
		}
		s.owners.set(job, u.ID)
		s.audit1(u, r, "wp.push", from+" -> "+live+" job "+job)
		http.Redirect(w, r, "/wp/"+live+"?job="+job+"#job", http.StatusSeeOther)
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
