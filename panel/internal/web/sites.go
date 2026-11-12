package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// jobOwners remembers which user started a job, so only that user may see its one-time password.
type jobOwners struct {
	mu sync.Mutex
	m  map[string]int64
}

func (j *jobOwners) set(job string, uid int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.m == nil {
		j.m = map[string]int64{}
	}
	j.m[job] = uid
}

func (j *jobOwners) owner(job string) (int64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	u, ok := j.m[job]
	return u, ok
}

// SetAgent connects the panel to nostek-agent.
func (s *Server) SetAgent(c *agent.Client) { s.ag = c }

func (s *Server) agentCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

var (
	jobRe   = regexp.MustCompile(`^j[0-9a-f]{16}$`)
	siteRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	entryRe = regexp.MustCompile(`^[a-z0-9-]{1,40}\.[0-9]{9,12}$`)
	splitRe = regexp.MustCompile(`[\s,;]+`)
)

type sitesData struct {
	Sites   []agent.Site
	Trash   []trashRow
	NoAgent bool
	Err     string
}

type trashRow struct {
	agent.Trashed
	DeletedAt, PurgeAt string
}

func (s *Server) sitesList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := sitesData{}
	list, err := s.ag.Sites(ctx)
	if err != nil {
		d.NoAgent = errors.Is(err, agent.ErrNoAgent)
		d.Err = err.Error()
	}
	d.Sites = list
	if tr, err := s.ag.Trash(ctx); err == nil {
		for _, t := range tr {
			d.Trash = append(d.Trash, trashRow{t, time.Unix(t.Deleted, 0).In(s.loc()).Format("02 Jan 2006 15:04"),
				time.Unix(t.Purge, 0).In(s.loc()).Format("02 Jan 2006 15:04")})
		}
	}
	s.render(w, r, "sites", page{Title: s.tr.T(s.lang(r), "nav.sites"), Active: "sites", Data: d, Flash: r.URL.Query().Get("done")})
}

type newData struct {
	Form    agent.Create
	Aliases string
	Catalog agent.Catalog
	Plan    *agent.Site
	NoAgent bool
}

func (s *Server) siteForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	cat, err := s.ag.Catalog(ctx)
	d := newData{Catalog: cat, NoAgent: err != nil, Form: agent.Create{Primary: "domain", Server: "apache", AdminEmail: userOf(r).Email}}
	for _, p := range cat.PHP {
		if p.Default {
			d.Form.PHP = p.Version
		}
	}
	s.render(w, r, "sitenew", page{Title: s.tr.T(s.lang(r), "sites.new"), Active: "sites", Data: d})
}

func formCreate(r *http.Request) (agent.Create, string) {
	f := r.PostForm
	low := func(k string) string { return strings.ToLower(strings.TrimSpace(f.Get(k))) }
	raw := strings.TrimSpace(f.Get("aliases"))
	c := agent.Create{Domain: low("domain"), Primary: low("primary"), Server: low("server"), PHP: strings.TrimSpace(f.Get("php")),
		AdminEmail: strings.TrimSpace(f.Get("admin_email")), Title: strings.TrimSpace(f.Get("title")), Aliases: []string{}}
	for _, a := range splitRe.Split(strings.ToLower(raw), -1) {
		if a != "" {
			c.Aliases = append(c.Aliases, a)
		}
	}
	return c, raw
}

// siteSubmit handles both steps: "plan" shows the approve card, "create" starts the job.
func (s *Server) siteSubmit(w http.ResponseWriter, r *http.Request) {
	c, raw := formCreate(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	cat, _ := s.ag.Catalog(ctx)
	d := newData{Form: c, Aliases: raw, Catalog: cat}
	fail := func(err error) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "sitenew", page{Title: s.tr.T(s.lang(r), "sites.new"), Active: "sites", Data: d, Error: err.Error()})
	}
	switch r.PostForm.Get("step") {
	case "plan":
		p, err := s.ag.Plan(ctx, c)
		if err != nil {
			fail(err)
			return
		}
		d.Plan = &p
		s.render(w, r, "sitenew", page{Title: s.tr.T(s.lang(r), "sites.approve"), Active: "sites", Data: d})
	case "create":
		job, site, err := s.ag.CreateSite(ctx, c)
		if err != nil {
			fail(err)
			return
		}
		s.owners.set(job, userOf(r).ID)
		s.audit1(userOf(r), r, "site.create", site.ID+" "+c.Domain+" "+c.Server+" php "+c.PHP)
		http.Redirect(w, r, "/sites/job/"+job, http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}

type jobData struct {
	Job      agent.Job
	Site     agent.Site
	Password string
	Mine     bool
	Steps    []stepRow
	Percent  int
}

// jobSteps is the order in which the agent reports the steps of a create job.
var jobSteps = []string{"files", "pull images", "start containers", "install WordPress"}

// stepRow is one line of the progress list: done, active, failed or todo.
type stepRow struct {
	Key   string `json:"key"`
	State string `json:"state"`
}

// jobProgress turns the agent's job (each step is reported when it starts) into the full list of
// steps with a state each, and the share of finished steps in percent.
func jobProgress(j agent.Job) ([]stepRow, int) {
	keys := append([]string{}, jobSteps...)
	for _, st := range j.Steps { // a step this panel does not know yet still shows up
		known := false
		for _, k := range keys {
			known = known || k == st
		}
		if !known {
			keys = append(keys, st)
		}
	}
	started := map[string]bool{}
	for _, st := range j.Steps {
		started[st] = true
	}
	last := ""
	if n := len(j.Steps); n > 0 {
		last = j.Steps[n-1]
	}
	rows, done := make([]stepRow, 0, len(keys)), 0
	for _, k := range keys {
		state := "todo"
		switch {
		case j.State == "done" && started[k]:
			state = "done"
		case k == last && j.State == "failed":
			state = "failed"
		case k == last && j.State == "running":
			state = "active"
		case started[k]:
			state = "done"
		}
		if state == "done" {
			done++
		}
		rows = append(rows, stepRow{k, state})
	}
	return rows, done * 100 / len(rows)
}

// siteJobStatus answers the job page's progress poll (JSON, no secrets).
func (s *Server) siteJobStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job")
	if !jobRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	j, err := s.ag.Job(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, pct := jobProgress(j)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		State   string    `json:"state"`
		Percent int       `json:"percent"`
		Steps   []stepRow `json:"steps"`
		Failed  bool      `json:"failed"`
	}{j.State, pct, rows, j.State == "failed"})
}

func (s *Server) siteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job")
	if !jobRe.MatchString(id) {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	j, err := s.ag.Job(ctx, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	site, _ := s.ag.Site(ctx, j.Site)
	owner, ok := s.owners.owner(id)
	steps, pct := jobProgress(j)
	s.render(w, r, "sitejob", page{Title: s.tr.T(s.lang(r), "sites.job"), Active: "sites",
		Data: jobData{Job: j, Site: site, Mine: ok && owner == userOf(r).ID, Steps: steps, Percent: pct}})
}

// siteSecret shows the new site's admin password once; the agent forgets it in the same step.
func (s *Server) siteSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job")
	owner, ok := s.owners.owner(id)
	if !jobRe.MatchString(id) || !ok || owner != userOf(r).ID {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	j, err := s.ag.Job(ctx, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	pw, err := s.ag.Secret(ctx, id)
	if err != nil {
		s.message(w, r, 410, "linkgone")
		return
	}
	site, _ := s.ag.Site(ctx, j.Site)
	s.audit1(userOf(r), r, "site.password_shown", j.Site+" admin password shown once")
	steps, pct := jobProgress(j)
	s.render(w, r, "sitejob", page{Title: s.tr.T(s.lang(r), "sites.job"), Active: "sites",
		Data: jobData{Job: j, Site: site, Password: pw, Mine: true, Steps: steps, Percent: pct}})
}

type siteData struct {
	Site    agent.Site
	Logs    string
	Service string
	Created string
	Stack   stackView
}

func (s *Server) siteShow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	site, err := s.ag.Site(ctx, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	d := siteData{Site: site, Created: time.Unix(site.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04")}
	if st, err := s.ag.Stack(ctx, id); err == nil {
		d.Stack = s.stackOf(st)
	} else {
		d.Stack = stackView{Err: err.Error()}
	}
	if svc := r.URL.Query().Get("logs"); svc == "web" || svc == "db" || (svc == "php" && site.Server == "nginx") {
		d.Service = svc
		if out, err := s.ag.Logs(ctx, id, svc, 200); err == nil {
			d.Logs = out
		} else {
			d.Logs = err.Error()
		}
	}
	s.render(w, r, "site", page{Title: site.Domain, Active: "sites", Data: d, Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) siteAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) {
		s.notFound(w, r)
		return
	}
	act := r.PostForm.Get("action")
	ctx, cancel := s.agentCtx(r, 6*time.Minute)
	defer cancel()
	site, err := s.ag.Site(ctx, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := "/sites/" + id
	switch act {
	case "start", "stop", "restart":
		err = s.ag.Action(ctx, id, act)
	case "restart-service":
		svc := r.PostForm.Get("service")
		if svc != "web" && svc != "db" && !(svc == "php" && site.Server == "nginx") {
			http.Error(w, "bad request", 400)
			return
		}
		err = s.ag.RestartService(ctx, id, svc)
		act = "restart_" + svc
	case "delete":
		if strings.TrimSpace(strings.ToLower(r.PostForm.Get("confirm"))) != site.Domain {
			http.Redirect(w, r, back+"?err="+url.QueryEscape(s.tr.T(s.lang(r), "sites.delete.mismatch")), http.StatusSeeOther)
			return
		}
		_, err = s.ag.Delete(ctx, id)
		back = "/sites"
	default:
		http.Error(w, "bad request", 400)
		return
	}
	if err != nil {
		s.audit1(userOf(r), r, "site."+act, id+" failed: "+err.Error())
		http.Redirect(w, r, "/sites/"+id+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "site."+act, id+" "+site.Domain)
	http.Redirect(w, r, back+"?done="+act, http.StatusSeeOther)
}

func (s *Server) siteRestore(w http.ResponseWriter, r *http.Request) {
	entry := r.PathValue("entry")
	if !entryRe.MatchString(entry) {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 6*time.Minute)
	defer cancel()
	id, err := s.ag.Restore(ctx, entry)
	if err != nil && id == "" {
		s.audit1(userOf(r), r, "site.restore", entry+" failed: "+err.Error())
		http.Redirect(w, r, "/sites?done=restore_failed", http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "site.restore", entry)
	http.Redirect(w, r, "/sites/"+id+"?done=restore", http.StatusSeeOther)
}
