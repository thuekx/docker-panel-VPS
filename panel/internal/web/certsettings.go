package web

// Certificates settings (CERT-01): the ClouDNS API sub-user the edge uses for wildcard certificates. The password
// goes to the agent once; the panel never stores, logs or shows it.

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type certSettingsData struct {
	C     agent.DNSConfig
	Since string
	Err   string
}

func (s *Server) certSettingsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := certSettingsData{}
	c, err := s.ag.DNS(ctx)
	if err != nil {
		d.Err = err.Error()
	}
	d.C = c
	if c.Since > 0 {
		d.Since = time.Unix(c.Since, 0).In(s.loc()).Format("02 Jan 2006 15:04")
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "certsettings", page{Title: s.tr.T(s.lang(r), "dns.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) certSettingsAction(w http.ResponseWriter, r *http.Request) {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	base := "/settings/certs"
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	ctx, cancel := s.agentCtx(r, 45*time.Second)
	defer cancel()
	switch f.Get("op") {
	case "set":
		id, pass := strings.TrimSpace(f.Get("sub_auth_id")), f.Get("password")
		if id == "" || pass == "" {
			back(s.tr.T(l, "dns.need"))
			return
		}
		if len(id) > 12 || len(pass) > 256 {
			back(s.tr.T(l, "dns.toolong"))
			return
		}
		if _, err := s.ag.SetDNS(ctx, id, pass); err != nil {
			s.audit1(u, r, "dns.set", "cloudns sub-user "+id+" refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "dns.set", "cloudns sub-user "+id)
		http.Redirect(w, r, base+"?done=set", http.StatusSeeOther)
	case "remove":
		if err := s.ag.RemoveDNS(ctx); err != nil {
			s.audit1(u, r, "dns.remove", "refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "dns.remove", "cloudns")
		http.Redirect(w, r, base+"?done=removed", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}
