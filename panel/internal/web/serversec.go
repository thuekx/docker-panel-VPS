package web

// Server security page (SRV-01, Settings → Server security): the server firewall and fail2ban. A firewall change
// is a trial the agent undoes by itself unless it is confirmed from a session that started after the change (sign
// in again in a new window): that proves the admin can still reach the panel. SSH ports can never be closed here.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

type serverSecData struct {
	V          agent.ServerSecurity
	Err        string
	Rules      []agent.FWRule // without the v6 twins of v4 rules
	TrialLeft  int64          // seconds
	CanConfirm bool           // this session started after the change
	Deadline   string
	AllowText  string
	IP         string // the visitor's address, for the allow-list hint
}

func (s *Server) serverSecPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.agentCtx(r, 25*time.Second)
	defer cancel()
	d := serverSecData{IP: s.clientIP(r)}
	v, err := s.ag.ServerSecurity(ctx)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	d.Rules = v.Rules
	d.AllowText = strings.Join(v.Allow, "\n")
	if t := v.Trial; t != nil {
		d.TrialLeft = t.Deadline - s.now().Unix()
		if d.TrialLeft < 0 {
			d.TrialLeft = 0
		}
		d.Deadline = time.Unix(t.Deadline, 0).In(s.loc()).Format("15:04:05")
		if sess, _ := s.currentSession(r); sess != nil && sess.CreatedAt.Unix() > t.Started {
			d.CanConfirm = true
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "serversec", page{Title: s.tr.T(s.lang(r), "srv.title"), Active: "settings", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) serverSecAction(w http.ResponseWriter, r *http.Request) {
	u, l, f := userOf(r), s.lang(r), r.PostForm
	base := "/settings/server"
	back := func(msg, anchor string) {
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+anchor, http.StatusSeeOther)
	}
	done := func(key, anchor string) { http.Redirect(w, r, base+"?done="+key+anchor, http.StatusSeeOther) }
	ctx, cancel := s.agentCtx(r, 40*time.Second)
	defer cancel()
	needPassword := func() bool {
		if !s.ipLimit.Allow("pw:" + s.clientIP(r)) {
			back(s.tr.T(l, "err.toomany"), "")
			return false
		}
		if !auth.CheckPassword(u.PwHash, f.Get("password")) {
			s.audit1(u, r, "server.security", "refused: wrong password")
			back(s.tr.T(l, "sbk.err.password"), "")
			return false
		}
		return true
	}
	switch op := f.Get("op"); op {
	case "allow", "deny", "delete":
		if !needPassword() {
			return
		}
		n, _ := strconv.Atoi(f.Get("rule"))
		ch := agent.FWChange{Op: op, Port: strings.TrimSpace(f.Get("port")), Proto: f.Get("proto"), From: strings.TrimSpace(f.Get("from")), Rule: n, Match: f.Get("match")}
		t, err := s.ag.FirewallChange(ctx, ch)
		if err != nil {
			s.audit1(u, r, "firewall.change", "failed: "+err.Error())
			back(err.Error(), "#firewall")
			return
		}
		s.audit1(u, r, "firewall.change", "trial: "+t.What)
		done("trial", "#trial")
	case "confirm":
		v, err := s.ag.ServerSecurity(ctx)
		if err != nil || v.Trial == nil {
			back(s.tr.T(l, "srv.err.notrial"), "")
			return
		}
		if sess, _ := s.currentSession(r); sess == nil || sess.CreatedAt.Unix() <= v.Trial.Started {
			back(s.tr.T(l, "srv.err.newsession"), "#trial")
			return
		}
		if err := s.ag.FirewallConfirm(ctx); err != nil {
			back(err.Error(), "")
			return
		}
		s.audit1(u, r, "firewall.confirm", v.Trial.What)
		done("confirmed", "#firewall")
	case "undo":
		if err := s.ag.FirewallUndo(ctx); err != nil {
			back(err.Error(), "")
			return
		}
		s.audit1(u, r, "firewall.undo", "")
		done("undone", "#firewall")
	case "unban":
		jail, ip := f.Get("jail"), f.Get("ip")
		if err := s.ag.Unban(ctx, jail, ip); err != nil {
			back(err.Error(), "#bans")
			return
		}
		s.audit1(u, r, "fail2ban.unban", jail+" "+ip)
		done("unbanned", "#bans")
	case "allowlist":
		if !needPassword() {
			return
		}
		var list []string
		for _, x := range strings.FieldsFunc(f.Get("allow"), func(c rune) bool { return c == '\n' || c == ',' || c == ' ' || c == '\r' || c == '\t' }) {
			list = append(list, x)
		}
		got, err := s.ag.SetFail2banAllow(ctx, list)
		if err != nil {
			back(err.Error(), "#allow")
			return
		}
		s.audit1(u, r, "fail2ban.allow", strings.Join(got, " "))
		done("allow", "#allow")
	default:
		http.Error(w, "bad request", 400)
	}
}
