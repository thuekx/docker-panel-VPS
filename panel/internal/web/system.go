package web

// System page (OPS-02, Settings → System): the core services with restart, the top processes and the drift of the
// files the panel manages for each website, with "Write back" after a confirmation.

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

var coreNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

type procRow struct {
	agent.Proc
	CPUText, MemText, RSSText string
}

type systemData struct {
	Services  []agent.CoreService
	SvcErr    string
	ByCPU     []procRow
	ByMem     []procRow
	Procs     agent.ProcView
	ProcErr   string
	MemTotal  string
	Drift     []agent.DriftSite
	DriftErr  string
	DriftN    int // websites with drift
	Unhealthy int
}

func procRows(ps []agent.Proc) []procRow {
	out := make([]procRow, 0, len(ps))
	for _, p := range ps {
		out = append(out, procRow{p, strconv.FormatFloat(p.CPU, 'f', 1, 64) + "%", strconv.FormatFloat(p.Mem, 'f', 1, 64) + "%", size(p.RSS)})
	}
	return out
}

func (s *Server) systemPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	var d systemData
	if v, err := s.ag.CoreServices(ctx); err != nil {
		d.SvcErr = err.Error()
	} else {
		d.Services = v
		for _, c := range v {
			if !c.OK {
				d.Unhealthy++
			}
		}
	}
	if v, err := s.ag.Processes(ctx); err != nil {
		d.ProcErr = err.Error()
	} else {
		d.Procs, d.ByCPU, d.ByMem, d.MemTotal = v, procRows(v.ByCPU), procRows(v.ByMem), size(v.MemTotal)
	}
	if v, err := s.ag.Drift(ctx); err != nil {
		d.DriftErr = err.Error()
	} else {
		d.Drift = v
		for _, x := range v {
			if x.Drift {
				d.DriftN++
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "system", page{Title: s.tr.T(s.lang(r), "sys.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) systemAction(w http.ResponseWriter, r *http.Request) {
	u, f := userOf(r), r.PostForm
	base := "/settings/system"
	back := func(msg, anchor string) {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+anchor, http.StatusSeeOther)
	}
	ctx, cancel := s.agentCtx(r, 6*time.Minute)
	defer cancel()
	switch f.Get("op") {
	case "restart":
		name := f.Get("name")
		if !coreNameRe.MatchString(name) {
			http.Error(w, "bad request", 400)
			return
		}
		if err := s.ag.RestartCore(ctx, name); err != nil {
			s.audit1(u, r, "server.restart", name+" failed: "+err.Error())
			back(err.Error(), "#services")
			return
		}
		s.audit1(u, r, "server.restart", name)
		done := "restart"
		if name == "agent" {
			done = "restart-agent"
		}
		http.Redirect(w, r, base+"?done="+done+"#services", http.StatusSeeOther)
	case "drift-fix":
		id := f.Get("site")
		if !siteRe.MatchString(id) {
			http.Error(w, "bad request", 400)
			return
		}
		if err := s.ag.DriftFix(ctx, id); err != nil {
			s.audit1(u, r, "server.drift.fix", id+" failed: "+err.Error())
			back(err.Error(), "#drift")
			return
		}
		s.audit1(u, r, "server.drift.fix", id)
		http.Redirect(w, r, base+"?done=drift#drift", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}
