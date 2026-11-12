package web

// Website backup page (BAK-01). nostek-agent makes, keeps, restores and prunes the archives; the panel only
// asks for them and streams a download through. A restore asks for the domain to be typed, like a delete.

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

var backupNameRe = regexp.MustCompile(`^(manual|daily|before-restore)-[0-9]{8}-[0-9]{6}\.tar\.gz$`)

type backupRow struct {
	agent.SiteBackup
	Human, When string
}

type backupData struct {
	Site    agent.Site
	B       agent.Backups
	Rows    []backupRow
	Err     string
	Free    string
	Job     *agent.Job // the job this page follows (running, or just finished)
	Steps   []stepRow
	Percent int
	Result  string // the backup a finished job made
	Keeps   []int
	Restore *backupRow // the backup whose restore form is open
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := backupData{Site: site, Keeps: []int{1, 3, 7, 14, 30}}
	b, err := s.ag.Backups(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.B = b
	if b.Free >= 0 && err == nil {
		d.Free = size(b.Free)
	}
	for _, x := range b.Backups {
		d.Rows = append(d.Rows, backupRow{x, size(x.Size), time.Unix(x.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04")})
	}
	for i := range d.Rows {
		if d.Rows[i].Name == r.URL.Query().Get("restore") {
			d.Restore = &d.Rows[i]
		}
	}
	// follow the job named in the address, or else the one running now
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && j.Site == site.ID && (j.Kind == "backup" || j.Kind == "restore") {
			d.Job = &j
		}
	}
	if d.Job == nil && b.Job != nil {
		d.Job = b.Job
	}
	if d.Job != nil {
		d.Steps, d.Percent = jobProgress(*d.Job)
		for _, st := range d.Job.Steps {
			if strings.HasPrefix(st, "done ") {
				d.Result = strings.TrimPrefix(st, "done ")
			}
		}
	}
	s.render(w, r, "backup", page{Title: s.tr.T(s.lang(r), "bak.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// backupAction starts a backup or a restore, deletes a backup or saves the schedule.
func (s *Server) backupAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u := r.PostForm, userOf(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/backup/" + site.ID
	fail := func(act string, err error) {
		s.audit1(u, r, "backup."+act, site.ID+" failed: "+err.Error())
		http.Redirect(w, r, base+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
	}
	name := f.Get("name")
	op := f.Get("op")
	if (op == "delete" || op == "restore") && !backupNameRe.MatchString(name) {
		http.Error(w, "bad request", 400)
		return
	}
	switch op {
	case "backup":
		job, err := s.ag.StartBackup(ctx, site.ID)
		if err != nil {
			fail("start", err)
			return
		}
		s.audit1(u, r, "backup.start", site.ID)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	case "schedule":
		keep, err := strconv.Atoi(f.Get("keep"))
		if err != nil || keep < 1 || keep > 30 {
			http.Error(w, "bad request", 400)
			return
		}
		daily := f.Get("daily") == "1"
		if err := s.ag.SetBackupSchedule(ctx, site.ID, daily, keep); err != nil {
			fail("schedule", err)
			return
		}
		s.audit1(u, r, "backup.schedule", site.ID+" daily="+strconv.FormatBool(daily)+" keep="+strconv.Itoa(keep))
		http.Redirect(w, r, base+"?done=schedule", http.StatusSeeOther)
	case "delete":
		if err := s.ag.DeleteBackup(ctx, site.ID, name); err != nil {
			fail("delete", err)
			return
		}
		s.audit1(u, r, "backup.delete", site.ID+" "+name)
		http.Redirect(w, r, base+"?done=delete", http.StatusSeeOther)
	case "restore":
		what := f.Get("what")
		if what != "all" && what != "files" && what != "database" {
			http.Error(w, "bad request", 400)
			return
		}
		if strings.TrimSpace(strings.ToLower(f.Get("confirm"))) != site.Domain {
			http.Redirect(w, r, base+"?err="+url.QueryEscape(s.tr.T(s.lang(r), "bak.restore.mismatch")), http.StatusSeeOther)
			return
		}
		job, err := s.ag.RestoreBackup(ctx, site.ID, name, what)
		if err != nil {
			fail("restore", err)
			return
		}
		s.audit1(u, r, "backup.restore", site.ID+" "+name+" "+what)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}

func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !backupNameRe.MatchString(name) {
		s.notFound(w, r)
		return
	}
	long(w, 3*time.Hour)
	ctx, cancel := s.agentCtx(r, 3*time.Hour)
	defer cancel()
	resp, err := s.ag.BackupFile(ctx, site.ID, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.audit1(userOf(r), r, "backup.download", site.ID+" "+name)
	relay(w, resp)
}
