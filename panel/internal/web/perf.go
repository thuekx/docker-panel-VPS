package web

// Performance page (PHP-01): the PHP version of a website (like cPanel MultiPHP Manager: a backup first, and the
// way back in one click) and Optimize Website (compression and browser cache at the edge). Both run as agent jobs
// the page follows.

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

var phpRe = regexp.MustCompile(`^[0-9]\.[0-9]{1,2}$`)

type perfData struct {
	Site     agent.Site
	V        agent.Perf
	Err      string
	Job      *agent.Job
	JobTitle string
	Steps    []stepRow
	Percent  int
	Running  bool
	Backup   string // the before-php backup a finished PHP change made
}

// phpSteps is the order of the steps of a PHP change of this website.
func phpSteps(site agent.Site) []string {
	var keys []string
	if !site.IsNative() {
		keys = append(keys, "pull new images")
	}
	if site.HasDB() {
		keys = append(keys, "copy database")
	}
	keys = append(keys, "copy files")
	switch {
	case site.IsNative():
		return append(keys, "restart PHP")
	case site.IsLaravel() || (site.AppName() == "php" && site.IsPostgres()):
		keys = append(keys, "build image")
	}
	return append(keys, "recreate containers")
}

func (s *Server) perfPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := perfData{Site: site}
	v, err := s.ag.Perf(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && j.Site == site.ID && (j.Kind == "php" || j.Kind == "optimize") {
			d.Job = &j
		}
	}
	if d.Job == nil && v.Job != nil && (v.Job.State == "running" || r.URL.Query().Get("job") != "") {
		d.Job = v.Job
	}
	if d.Job != nil {
		d.Running = d.Job.State == "running"
		d.JobTitle = "perf.opt.job"
		if d.Job.Kind == "php" {
			d.JobTitle = "perf.php.job"
			for _, st := range d.Job.Steps {
				if name, ok := strings.CutPrefix(st, "done "); ok && backupNameRe.MatchString(name) {
					d.Backup = name
				}
			}
		}
		d.Steps, d.Percent = jobProgress(*d.Job, site)
	}
	s.render(w, r, "perf", page{Title: s.tr.T(s.lang(r), "perf.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) perfAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u := r.PostForm, userOf(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/perf/" + site.ID
	back := func(msg, anchor string) {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+anchor, http.StatusSeeOther)
	}
	switch f.Get("op") {
	case "php":
		v := f.Get("php")
		if !phpRe.MatchString(v) {
			back(s.tr.T(s.lang(r), "perf.php.need"), "#php")
			return
		}
		detail := site.ID + " " + site.PHP + " -> " + v
		job, err := s.ag.StartPHPChange(ctx, site.ID, v)
		if err != nil {
			s.audit1(u, r, "site.php", detail+" failed: "+err.Error())
			back(err.Error(), "#php")
			return
		}
		s.owners.set(job, u.ID)
		s.audit1(u, r, "site.php", detail)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	case "optimize":
		days, _ := strconv.Atoi(f.Get("days"))
		c := agent.SecurityChange{Op: "optimize-set", Compress: f.Get("compress") == "1", Days: days}
		detail := site.ID + " compress " + strconv.FormatBool(c.Compress) + " cache " + strconv.Itoa(days) + " days"
		job, err := s.ag.ChangeSecurity(ctx, site.ID, c)
		if err != nil {
			s.audit1(u, r, "site.optimize", detail+" failed: "+err.Error())
			back(err.Error(), "#optimize")
			return
		}
		if job == "" {
			http.Redirect(w, r, base+"?done=same#optimize", http.StatusSeeOther)
			return
		}
		s.audit1(u, r, "site.optimize", detail)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	case "header-set", "header-remove":
		c := agent.SecurityChange{Op: f.Get("op"), Path: strings.TrimSpace(f.Get("path")), Name: strings.TrimSpace(f.Get("name")), Value: f.Get("value")}
		if c.Path == "" {
			c.Path = "/"
		}
		if c.Name == "" || len(c.Name) > 64 || len(c.Path) > 200 || len(c.Value) > 4096 {
			back(s.tr.T(s.lang(r), "hdr.need"), "#headers")
			return
		}
		detail := site.ID + " " + c.Op + " " + c.Path + " " + c.Name // the value stays out of the log
		job, err := s.ag.ChangeSecurity(ctx, site.ID, c)
		if err != nil {
			s.audit1(u, r, "site.headers", detail+" failed: "+err.Error())
			back(err.Error(), "#headers")
			return
		}
		if job == "" {
			http.Redirect(w, r, base+"?done=same#headers", http.StatusSeeOther)
			return
		}
		s.audit1(u, r, "site.headers", detail)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	default:
		back("unknown action", "")
	}
}
