package web

// Scheduled server backups and backup destinations (BAK-03), on the Server backup page. The passphrase for
// scheduled backups goes to the agent once, which keeps only the public key it gives; destination secrets go to the
// agent and never come back. Changing the schedule or a destination asks for the account password.

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

var destIDRe = regexp.MustCompile(`^d[0-9a-f]{8}$`)

type destRow struct {
	agent.BackupDest
	Where        string
	LastWhen     string
	TestWhen     string
	SecretMasked string
}

type planData struct {
	P        agent.BackupPlan
	Err      string
	Hours    []int
	Days     []int
	NextWhen string
	LastWhen string
	Dests    []destRow
	Edit     *destRow // the destination being changed (?dest=)
	EditKind string   // kind of a new destination (?add=s3|sftp)
}

func (s *Server) planData(ctx context.Context, r *http.Request) planData {
	d := planData{Days: []int{1, 2, 3, 4, 5, 6, 0}}
	for h := range 24 {
		d.Hours = append(d.Hours, h)
	}
	p, err := s.ag.BackupPlan(ctx)
	if err != nil {
		d.Err = err.Error()
		return d
	}
	d.P = p
	when := func(u int64) string {
		if u == 0 {
			return ""
		}
		return time.Unix(u, 0).In(s.loc()).Format("02 Jan 2006 15:04")
	}
	d.NextWhen, d.LastWhen = when(p.Next), when(p.LastRun)
	for _, x := range p.Dests {
		row := destRow{BackupDest: x}
		if x.Kind == "s3" {
			row.Where = x.Endpoint + "/" + x.Bucket
			if x.Prefix != "" {
				row.Where += "/" + x.Prefix
			}
		} else {
			row.Where = x.User + "@" + x.Host + ":" + strconv.Itoa(x.Port) + "/" + x.Path
		}
		if x.Last != nil {
			row.LastWhen = when(x.Last.At)
		}
		if x.Test != nil {
			row.TestWhen = when(x.Test.At)
		}
		d.Dests = append(d.Dests, row)
		if id := r.URL.Query().Get("dest"); id == x.ID {
			e := row
			d.Edit = &e
		}
	}
	if k := r.URL.Query().Get("add"); k == "s3" || k == "sftp" {
		d.EditKind = k
	}
	return d
}

// planAction handles the BAK-03 operations of the Server backup page; it reports whether op was one of them.
func (s *Server) planAction(w http.ResponseWriter, r *http.Request, op string) bool {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	base := "/settings/backup"
	back := func(msg, anchor string) {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+anchor, http.StatusSeeOther)
	}
	done := func(what, anchor string) {
		http.Redirect(w, r, base+"?done="+what+anchor, http.StatusSeeOther)
	}
	password := func(action string) bool {
		if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
			back(s.tr.T(l, "err.toomany"), "#plan")
			return false
		}
		if !auth.CheckPassword(u.PwHash, f.Get("password")) {
			s.audit1(u, r, action, "refused: wrong password")
			back(s.tr.T(l, "sbk.err.password"), "#plan")
			return false
		}
		return true
	}
	ctx, cancel := s.agentCtx(r, 2*time.Minute)
	defer cancel()
	switch op {
	case "schedule":
		every := f.Get("every")
		hour, err1 := strconv.Atoi(f.Get("hour"))
		day, err2 := strconv.Atoi(f.Get("weekday"))
		if (every != "" && every != "daily" && every != "weekly") || err1 != nil || err2 != nil || hour < 0 || hour > 23 || day < 0 || day > 6 {
			http.Error(w, "bad request", 400)
			return true
		}
		if !password("server.backup.schedule") {
			return true
		}
		pass := f.Get("passphrase")
		if pass != "" {
			if utf8.RuneCountInString(pass) < minPassphrase || len(pass) > 1024 {
				back(s.tr.T(l, "sbk.err.short", minPassphrase), "#plan")
				return true
			}
			if pass != f.Get("passphrase2") {
				back(s.tr.T(l, "sbk.err.match"), "#plan")
				return true
			}
		}
		if err := s.ag.SetServerSchedule(ctx, every, hour, day, pass); err != nil {
			s.audit1(u, r, "server.backup.schedule", "failed: "+err.Error())
			back(err.Error(), "#plan")
			return true
		}
		detail := "off"
		if every != "" {
			detail = every + " " + strconv.Itoa(hour) + ":00 WIB"
		}
		if pass != "" {
			detail += ", new passphrase"
		}
		s.audit1(u, r, "server.backup.schedule", detail)
		done("schedule", "#plan")
	case "dest-save":
		keep, _ := strconv.Atoi(f.Get("keep"))
		port, _ := strconv.Atoi(f.Get("port"))
		in := agent.DestInput{ID: f.Get("id"), Kind: f.Get("kind"), Name: strings.TrimSpace(f.Get("name")), On: f.Get("on") == "1", Keep: keep,
			Endpoint: strings.TrimSpace(f.Get("endpoint")), Region: strings.TrimSpace(f.Get("region")), Bucket: strings.TrimSpace(f.Get("bucket")),
			Prefix: strings.TrimSpace(f.Get("prefix")), AccessKey: strings.TrimSpace(f.Get("access_key")), Secret: f.Get("secret"),
			Host: strings.TrimSpace(f.Get("host")), Port: port, User: strings.TrimSpace(f.Get("user")), Path: strings.TrimSpace(f.Get("path"))}
		if (in.ID != "" && !destIDRe.MatchString(in.ID)) || (in.Kind != "s3" && in.Kind != "sftp") {
			http.Error(w, "bad request", 400)
			return true
		}
		if !password("server.backup.dest") {
			return true
		}
		id, err := s.ag.SaveDest(ctx, in)
		if err != nil {
			s.audit1(u, r, "server.backup.dest", in.Kind+" "+in.Name+" failed: "+err.Error())
			back(err.Error(), "#dests")
			return true
		}
		s.audit1(u, r, "server.backup.dest", id+" "+in.Kind+" "+in.Name)
		done("dest", "#dests")
	case "dest-delete", "dest-test":
		id := f.Get("id")
		if !destIDRe.MatchString(id) {
			http.Error(w, "bad request", 400)
			return true
		}
		if op == "dest-delete" {
			if err := s.ag.DeleteDest(ctx, id); err != nil {
				back(err.Error(), "#dests")
				return true
			}
			s.audit1(u, r, "server.backup.dest.delete", id)
			done("destdelete", "#dests")
			return true
		}
		res, err := s.ag.TestDest(ctx, id)
		if err != nil {
			back(err.Error(), "#dests")
			return true
		}
		result := "ok"
		if !res.OK {
			result = "failed"
		}
		s.audit1(u, r, "server.backup.dest.test", id+" "+result)
		if !res.OK {
			back(s.tr.T(l, "dst.test.failed", res.Msg), "#dests")
			return true
		}
		done("desttest", "#dests")
	case "ssh-key":
		if _, err := s.ag.BackupSSHKey(ctx); err != nil {
			back(err.Error(), "#dests")
			return true
		}
		done("sshkey", "#dests")
	case "send":
		name, dest := f.Get("name"), f.Get("dest")
		if !serverBackupRe.MatchString(name) || !destIDRe.MatchString(dest) {
			http.Error(w, "bad request", 400)
			return true
		}
		job, err := s.ag.SendServerBackup(ctx, name, dest)
		if err != nil {
			s.audit1(u, r, "server.backup.send", name+" "+dest+" failed: "+err.Error())
			back(err.Error(), "#list")
			return true
		}
		s.audit1(u, r, "server.backup.send", name+" "+dest+" job "+job)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	default:
		return false
	}
	return true
}

// BackupTick starts the scheduled server backup when the agent says one is due: the panel writes its database copy
// first, like for a backup made by hand.
func (s *Server) BackupTick(ctx context.Context) {
	cp := s.panelCopy()
	if cp == "" {
		return
	}
	actx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	p, err := s.ag.BackupPlan(actx)
	if err != nil || !p.Due {
		return
	}
	if err := s.st.Snapshot(cp); err != nil {
		return
	}
	if _, err := s.ag.RunScheduledBackup(actx, s.cfg.Version); err != nil {
		_ = os.Remove(cp)
	}
}

// BackupLoop runs BackupTick every five minutes until ctx ends.
func (s *Server) BackupLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.BackupTick(ctx)
		}
	}
}
