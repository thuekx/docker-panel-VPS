package api

// Server security routes (SRV-01): the firewall with a trial, fail2ban bans and its allow-list.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) serverSecurity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, 200, s.M.ServerSecurityView(ctx))
}

func (s *Server) firewallChange(w http.ResponseWriter, r *http.Request) {
	var c sites.FWChange
	if err := decode(r, &c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	t, err := s.M.FirewallChange(ctx, c)
	if err != nil {
		s.fail(w, r, "firewall", "", err)
		return
	}
	s.note(r, "firewall", "", "ok", "trial: "+t.What)
	writeJSON(w, 200, t)
}

func (s *Server) firewallConfirm(w http.ResponseWriter, r *http.Request) {
	if err := s.M.FirewallConfirm(); err != nil {
		s.fail(w, r, "firewall-confirm", "", err)
		return
	}
	s.note(r, "firewall-confirm", "", "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) firewallUndo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.M.FirewallUndo(ctx, "undone from the panel"); err != nil {
		s.fail(w, r, "firewall-undo", "", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) fail2banUnban(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Jail string `json:"jail"`
		IP   string `json:"ip"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.M.Unban(ctx, a.Jail, a.IP); err != nil {
		s.fail(w, r, "unban", "", err)
		return
	}
	s.note(r, "unban", "", "ok", a.Jail+" "+a.IP)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) fail2banAllow(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Allow []string `json:"allow"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := s.M.SetFail2banAllow(ctx, a.Allow)
	if err != nil {
		s.fail(w, r, "fail2ban-allow", "", err)
		return
	}
	s.note(r, "fail2ban-allow", "", "ok", strings.Join(list, " "))
	writeJSON(w, 200, map[string][]string{"allow": list})
}
