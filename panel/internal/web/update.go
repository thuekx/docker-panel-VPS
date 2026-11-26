package web

// Update page (UPD-01, Settings → Update). The agent lists the releases and starts the updater; the panel writes a
// copy of its database first (for a roll-back) and, when a server backup is asked for, the copy that backup takes.
// Starting an update asks for the account password. The page follows the run by polling a small status answer, and
// keeps polling while the panel itself restarts.

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

var releaseTagRe = regexp.MustCompile(`^v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

type updateRelease struct {
	agent.Release
	When string
}

type updateData struct {
	V       agent.UpdateView
	Newer   []updateRelease
	Older   []updateRelease
	Err     string
	Min     int
	Busy    bool // an update or its backup is running
	Started string
	Ended   string
}

func updateBusy(v agent.UpdateView) bool {
	if v.Job != nil && v.Job.State == "running" {
		return true
	}
	return v.Status != nil && (v.Status.Running || v.Status.Phase == "STARTING")
}

func (s *Server) updatePage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 25*time.Second)
	defer cancel()
	d := updateData{Min: minPassphrase}
	v, err := s.ag.Update(ctx, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		d.Err = err.Error()
	}
	d.V, d.Busy = v, updateBusy(v)
	for _, x := range v.Releases {
		row := updateRelease{x, ""}
		if !x.Published.IsZero() {
			row.When = x.Published.In(s.loc()).Format("02 Jan 2006")
		}
		if x.Newer {
			d.Newer = append(d.Newer, row)
		} else if len(d.Older) < 3 {
			d.Older = append(d.Older, row)
		}
	}
	if st := v.Status; st != nil {
		if st.Started > 0 {
			d.Started = time.Unix(st.Started, 0).In(s.loc()).Format("02 Jan 2006 15:04")
		}
		if st.Ended > 0 {
			d.Ended = time.Unix(st.Ended, 0).In(s.loc()).Format("15:04")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "update", page{Title: s.tr.T(s.lang(r), "upd.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// updateStatus is what the page polls while an update runs.
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	v, err := s.ag.Update(ctx, false)
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		writeJSONStatus(w, 502, map[string]string{"error": "agent"})
		return
	}
	out := map[string]any{"busy": updateBusy(v), "current": v.Current}
	if st := v.Status; st != nil {
		out["phase"], out["tag"], out["result"], out["lines"] = st.Phase, st.Tag, st.Result, st.Lines
	}
	if j := v.Job; j != nil {
		out["job"] = map[string]any{"state": j.State, "steps": j.Steps, "progress": j.Progress, "error": j.Error}
	}
	writeJSONStatus(w, 200, out)
}

func (s *Server) updatePanelCopy(name string) string {
	if s.st.Path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.st.Path), name)
}

func (s *Server) updateAction(w http.ResponseWriter, r *http.Request) {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	back := func(msg string) {
		http.Redirect(w, r, "/settings/update?err="+url.QueryEscape(msg)+"#start", http.StatusSeeOther)
	}
	if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
		back(s.tr.T(l, "err.toomany"))
		return
	}
	tag := f.Get("tag")
	if !releaseTagRe.MatchString(tag) {
		back(s.tr.T(l, "upd.err.tag"))
		return
	}
	if !auth.CheckPassword(u.PwHash, f.Get("password")) {
		s.audit1(u, r, "panel.update", "refused: wrong password")
		back(s.tr.T(l, "sbk.err.password"))
		return
	}
	pass := ""
	if f.Get("backup") == "1" {
		pass = f.Get("passphrase")
		if utf8.RuneCountInString(pass) < minPassphrase || len(pass) > 1024 {
			back(s.tr.T(l, "sbk.err.short", minPassphrase))
			return
		}
		if pass != f.Get("passphrase2") {
			back(s.tr.T(l, "sbk.err.match"))
			return
		}
	}
	upd := s.updatePanelCopy("update.db")
	if upd == "" || s.st.Snapshot(upd) != nil {
		s.audit1(u, r, "panel.update", "failed: database copy")
		back(s.tr.T(l, "sbk.err.copy"))
		return
	}
	if pass != "" {
		if s.st.Snapshot(s.panelCopy()) != nil {
			_ = os.Remove(upd)
			back(s.tr.T(l, "sbk.err.copy"))
			return
		}
	}
	ctx, cancel := s.agentCtx(r, 40*time.Second)
	defer cancel()
	job, err := s.ag.StartUpdate(ctx, tag, pass, s.cfg.Version)
	if err != nil {
		_ = os.Remove(upd)
		if pass != "" {
			_ = os.Remove(s.panelCopy())
		}
		s.audit1(u, r, "panel.update", tag+" failed: "+err.Error())
		back(err.Error())
		return
	}
	detail := tag + " from " + s.cfg.Version + ", job " + job
	if pass != "" {
		detail += ", server backup first"
	}
	s.audit1(u, r, "panel.update", detail)
	http.Redirect(w, r, "/settings/update#run", http.StatusSeeOther)
}
