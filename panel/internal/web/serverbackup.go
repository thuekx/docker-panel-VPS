package web

// Server backup page (BAK-02, Settings). The panel writes a consistent copy of its database next to it, then asks
// the agent to seal that copy with every website into one file. The passphrase goes to the agent once and is never
// stored, logged or shown; making a backup also asks for the account password, since the file holds the whole
// server.

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

var serverBackupRe = regexp.MustCompile(`^nostek-server-[0-9]{8}-[0-9]{6}\.npbk$`)

// minPassphrase matches the agent's rule.
const minPassphrase = 12

type serverBackupRow struct {
	agent.ServerBackup
	Human, When string
}

type serverBackupData struct {
	B          agent.ServerBackups
	Rows       []serverBackupRow
	Err        string
	Free, Need string
	Low        bool // less free space than a backup needs
	Job        *agent.Job
	Steps      []stepRow
	Percent    int
	Result     string
	Min        int
	Host       string   // this panel's name, for the restore command
	Plan       planData // BAK-03
}

func (s *Server) serverBackupPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := serverBackupData{Min: minPassphrase, Host: r.Host}
	d.Plan = s.planData(ctx, r)
	b, err := s.ag.ServerBackups(ctx)
	if err != nil {
		d.Err = err.Error()
	}
	d.B = b
	if err == nil && b.Free >= 0 {
		d.Free, d.Need, d.Low = size(b.Free), size(b.Need), b.Free < b.Need
	}
	for _, x := range b.Backups {
		d.Rows = append(d.Rows, serverBackupRow{x, size(x.Size), time.Unix(x.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04")})
	}
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && (j.Kind == "server-backup" || j.Kind == "server-upload") {
			d.Job = &j
		}
	}
	if d.Job == nil && b.Job != nil {
		d.Job = b.Job
	}
	if d.Job != nil {
		d.Steps, d.Percent = jobProgress(*d.Job, agent.Site{})
		for _, st := range d.Job.Steps {
			if strings.HasPrefix(st, "done ") {
				d.Result = strings.TrimPrefix(st, "done ")
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "serverbackup", page{Title: s.tr.T(s.lang(r), "sbk.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// panelCopy is where the panel writes its database copy for the agent (the agent's PanelCopy).
func (s *Server) panelCopy() string {
	if s.st.Path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.st.Path), "server-backup.db")
}

func (s *Server) serverBackupAction(w http.ResponseWriter, r *http.Request) {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	base := "/settings/backup"
	back := func(msg string) {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	if s.planAction(w, r, f.Get("op")) {
		return
	}
	switch f.Get("op") {
	case "create":
		if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
			back(s.tr.T(l, "err.toomany"))
			return
		}
		if !auth.CheckPassword(u.PwHash, f.Get("password")) {
			s.audit1(u, r, "server.backup", "refused: wrong password")
			back(s.tr.T(l, "sbk.err.password"))
			return
		}
		pass := f.Get("passphrase")
		if utf8.RuneCountInString(pass) < minPassphrase || len(pass) > 1024 {
			back(s.tr.T(l, "sbk.err.short", minPassphrase))
			return
		}
		if pass != f.Get("passphrase2") {
			back(s.tr.T(l, "sbk.err.match"))
			return
		}
		cp := s.panelCopy()
		if cp == "" {
			back(s.tr.T(l, "sbk.err.copy"))
			return
		}
		if err := s.st.Snapshot(cp); err != nil {
			s.audit1(u, r, "server.backup", "failed: database copy")
			back(s.tr.T(l, "sbk.err.copy"))
			return
		}
		job, err := s.ag.StartServerBackup(ctx, pass, s.cfg.Version)
		if err != nil {
			_ = os.Remove(cp)
			s.audit1(u, r, "server.backup", "failed: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "server.backup", "job "+job)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	case "delete":
		name := f.Get("name")
		if !serverBackupRe.MatchString(name) {
			http.Error(w, "bad request", 400)
			return
		}
		if err := s.ag.DeleteServerBackup(ctx, name); err != nil {
			s.audit1(u, r, "server.backup.delete", name+" failed: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "server.backup.delete", name)
		http.Redirect(w, r, base+"?done=delete", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}

func (s *Server) serverBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !serverBackupRe.MatchString(name) {
		s.notFound(w, r)
		return
	}
	long(w, 6*time.Hour)
	ctx, cancel := s.agentCtx(r, 6*time.Hour)
	defer cancel()
	resp, err := s.ag.ServerBackupFile(ctx, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.audit1(userOf(r), r, "server.backup.download", name)
	relay(w, resp)
}
