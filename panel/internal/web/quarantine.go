package web

// Quarantine page (SEC-02, Settings → Quarantine): files a malware scan found and that were moved aside. Each shows
// where it came from and what was found; it can be put back or removed for good.

import (
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

var qidRe = regexp.MustCompile(`^q[0-9a-f]{16}$`)

type qRow struct {
	agent.QFile
	When, Human string
}

func (s *Server) quarantinePage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	var d struct {
		Rows []qRow
		Err  string
	}
	list, err := s.ag.Quarantine(ctx)
	if err != nil {
		d.Err = err.Error()
	}
	for _, q := range list {
		d.Rows = append(d.Rows, qRow{q, time.Unix(q.At, 0).In(s.loc()).Format("02 Jan 2006 15:04"), size(q.Size)})
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "quarantine", page{Title: s.tr.T(s.lang(r), "qr.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) quarantineAction(w http.ResponseWriter, r *http.Request) {
	u, f := userOf(r), r.PostForm
	id := f.Get("id")
	if !qidRe.MatchString(id) {
		http.Error(w, "bad request", 400)
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	var q agent.QFile
	var err error
	op := f.Get("op")
	switch op {
	case "restore":
		q, err = s.ag.QuarantineRestore(ctx, id)
	case "delete":
		q, err = s.ag.QuarantineDelete(ctx, id)
	default:
		http.Error(w, "bad request", 400)
		return
	}
	if err != nil {
		s.audit1(u, r, "quarantine."+op, id+" failed: "+err.Error())
		http.Redirect(w, r, "/settings/quarantine?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "quarantine."+op, q.Site+" "+q.Path)
	http.Redirect(w, r, "/settings/quarantine?done="+op, http.StatusSeeOther)
}
