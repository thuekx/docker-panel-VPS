package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	// APP-01: fields that do not belong to the chosen app are dropped, so a hidden field never blocks a request
	switch app := low("app"); app {
	case "", "wordpress":
	case "static":
		c.App, c.Server, c.PHP, c.AdminEmail = app, "nginx", "", ""
	case "laravel": // always Nginx + PHP-FPM with its own MariaDB
		c.App, c.Server, c.AdminEmail, c.Version = app, "nginx", "", strings.TrimSpace(f.Get("version"))
	case "node": // APP-02: built from Git, no web server or PHP choice
		c.App, c.Server, c.PHP, c.AdminEmail = app, "", "", ""
		c.Version = strings.TrimSpace(f.Get("node"))
		c.Repo, c.Branch = strings.TrimSpace(f.Get("repo")), strings.TrimSpace(f.Get("branch"))
		c.Build, c.Start = strings.TrimSpace(f.Get("build")), strings.TrimSpace(f.Get("start"))
		c.Dir = strings.Trim(strings.TrimSpace(f.Get("dir")), "/")
		if low("source") == "files" {
			c.Source, c.Repo, c.Branch = "files", "", ""
		}
	default: // php, or anything else for the agent to refuse
		c.App, c.AdminEmail, c.Database = app, "", f.Get("database") == "1"
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
		s.audit1(userOf(r), r, "site.create", site.ID+" "+c.Domain+" "+site.AppName()+" "+c.Server+" php "+c.PHP)
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

// laravelSteps is the order of a Laravel create; the agent reports "build PHP image" right after "files".
var laravelSteps = []string{"files", "build PHP image", "pull images", "install Laravel", "start containers"}

// nodeSteps is the order of a Node.js create (APP-02); the agent reports "fetch and build" after the image pull.
var nodeSteps = []string{"files", "pull images", "fetch and build", "start new release"}

// jobKindSteps is the order of the steps of the other jobs (BAK-01, APP-02 deploy).
var jobKindSteps = map[string][]string{
	"deploy":  {"fetch and build", "start new release"},
	"backup":  {"copy database", "copy files"},
	"restore": {"safety database", "safety files", "restore database", "restore files", "switch files"},
}

// stepRow is one line of the progress list: done, active, failed or todo.
type stepRow struct {
	Key   string `json:"key"`
	State string `json:"state"`
}

// jobProgress turns the agent's job (each step is reported when it starts) into the full list of
// steps with a state each, and the share of finished steps in percent.
func jobProgress(j agent.Job) ([]stepRow, int) {
	keys := append([]string{}, jobKindSteps[j.Kind]...)
	if j.Kind == "" || j.Kind == "create" {
		keys = append([]string{}, jobSteps...)
		for _, st := range j.Steps {
			if st == "build PHP image" {
				keys = append([]string{}, laravelSteps...)
			}
			if st == "fetch and build" {
				keys = append([]string{}, nodeSteps...)
			}
		}
	}
	if j.State == "done" { // only what ran: a static site or a database-only restore skips steps
		keys = nil // a finished restore of the database only never switched files: show what ran
	}
	steps := make([]string, 0, len(j.Steps))
	for _, st := range j.Steps {
		if !strings.HasPrefix(st, "done ") { // "done <name>" names the result, it is not a step
			steps = append(steps, st)
		}
	}
	j.Steps = steps
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
	if len(rows) == 0 {
		if j.State == "done" {
			return rows, 100
		}
		return rows, 0
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
	Artisan *artisanResult // the artisan command just run (Laravel)
	Deploys []deployRow    // APP-02: deploy history of a Node.js website
	NoHist  string         // why the history could not be read
	Env     []string       // names of the app's environment values
}

// deployRow is one deploy as the Overview shows it.
type deployRow struct {
	agent.Deploy
	When  string
	Short string // first 7 characters of the commit
	Took  string // build + start time
	Kept  bool   // the release folder is still there (live or ok)
}

func (s *Server) deployRows(ds []agent.Deploy) []deployRow {
	rows := make([]deployRow, 0, len(ds))
	for _, d := range ds {
		r := deployRow{Deploy: d, When: time.Unix(d.Started, 0).In(s.loc()).Format("02 Jan 2006 15:04"),
			Kept: d.Result == "live" || d.Result == "ok"}
		if len(d.Commit) >= 7 {
			r.Short = d.Commit[:7]
		}
		if d.Ended > d.Started {
			r.Took = (time.Duration(d.Ended-d.Started) * time.Second).String()
		}
		rows = append(rows, r)
	}
	return rows
}

// artisanResult is the output of one artisan command shown on the Overview.
type artisanResult struct {
	Op, Output, Err string
}

// artisanOps are the artisan commands the panel offers; the agent keeps the same fixed list.
var artisanOps = []string{"migrate", "optimize", "optimize-clear", "queue-restart"}

func (s *Server) siteShow(w http.ResponseWriter, r *http.Request) {
	s.showSite(w, r, nil)
}

func (s *Server) showSite(w http.ResponseWriter, r *http.Request, art *artisanResult) {
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
	d := siteData{Site: site, Created: time.Unix(site.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04"), Artisan: art}
	if st, err := s.ag.Stack(ctx, id); err == nil {
		d.Stack = s.stackOf(st)
	} else {
		d.Stack = stackView{Err: err.Error()}
	}
	if site.IsNode() {
		if h, err := s.ag.Deploys(ctx, id); err == nil {
			d.Deploys = s.deployRows(h)
		} else {
			d.NoHist = err.Error()
		}
		d.Env, _ = s.ag.EnvNames(ctx, id)
	}
	if svc := r.URL.Query().Get("logs"); siteService(site, svc) {
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
		if !siteService(site, svc) {
			http.Error(w, "bad request", 400)
			return
		}
		err = s.ag.RestartService(ctx, id, svc)
		act = "restart_" + svc
	case "artisan":
		op := r.PostForm.Get("op")
		if !site.IsLaravel() || !slices.Contains(artisanOps, op) {
			http.Error(w, "bad request", 400)
			return
		}
		out, err := s.ag.Artisan(ctx, id, op)
		res := &artisanResult{Op: op, Output: out}
		if err != nil {
			res.Err = err.Error()
			s.audit1(userOf(r), r, "site.artisan", id+" "+op+" failed: "+err.Error())
		} else {
			s.audit1(userOf(r), r, "site.artisan", id+" "+op)
		}
		s.showSite(w, r, res)
		return
	case "deploy":
		if !site.IsNode() {
			http.Error(w, "bad request", 400)
			return
		}
		job, err := s.ag.StartDeploy(ctx, id)
		if err != nil {
			s.audit1(userOf(r), r, "site.deploy", id+" failed: "+err.Error())
			http.Redirect(w, r, back+"?err="+url.QueryEscape(err.Error())+"#deploy", http.StatusSeeOther)
			return
		}
		s.owners.set(job, userOf(r).ID)
		s.audit1(userOf(r), r, "site.deploy", id+" "+site.Domain)
		http.Redirect(w, r, "/sites/job/"+job, http.StatusSeeOther)
		return
	case "rollback":
		n, cerr := strconv.Atoi(r.PostForm.Get("release"))
		if !site.IsNode() || cerr != nil || n < 1 {
			http.Error(w, "bad request", 400)
			return
		}
		err = s.ag.Rollback(ctx, id, n)
		act = "rollback"
	case "env-set", "env-delete":
		if !site.IsNode() {
			http.Error(w, "bad request", 400)
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("name"))
		if act == "env-set" {
			err = s.ag.SetEnv(ctx, id, map[string]string{name: r.PostForm.Get("value")}, nil)
		} else {
			err = s.ag.SetEnv(ctx, id, nil, []string{name})
		}
		if err != nil {
			s.audit1(userOf(r), r, "site."+act, id+" "+name+" failed: "+err.Error())
			http.Redirect(w, r, back+"?err="+url.QueryEscape(err.Error())+"#env", http.StatusSeeOther)
			return
		}
		s.audit1(userOf(r), r, "site."+act, id+" "+name)
		http.Redirect(w, r, back+"?done="+act+"#env", http.StatusSeeOther)
		return
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

// siteService is true for a container the website has: web always, php behind Nginx, db with a database, and the
// queue worker and scheduler of a Laravel website.
func siteService(site agent.Site, svc string) bool {
	switch svc {
	case "web":
		return !site.IsNode()
	case "app":
		return site.IsNode()
	case "php":
		return site.Server == "nginx" && site.AppName() != "static"
	case "db":
		return site.HasDB()
	case "queue", "scheduler":
		return site.IsLaravel()
	}
	return false
}

// buildLog shows the build log of one release of a Node.js website (APP-02).
func (s *Server) buildLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if !siteRe.MatchString(id) || err != nil || n < 1 {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	site, err := s.ag.Site(ctx, id)
	if err != nil || !site.IsNode() {
		s.notFound(w, r)
		return
	}
	l, err := s.ag.BuildLog(ctx, id, n)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.render(w, r, "buildlog", page{Title: site.Domain, Active: "sites", Data: buildLogData{Site: site, Release: n, Log: l}})
}

type buildLogData struct {
	Site    agent.Site
	Release int
	Log     string
}
