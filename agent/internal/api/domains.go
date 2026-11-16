package api

// Domain routes (DOM-01): the DNS and certificate check of a website's names, and changes of its names and
// redirects. Names and paths are not secret; the audit log records them.

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) domains(w http.ResponseWriter, r *http.Request) {
	d, err := s.M.Domains(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) domainsChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c sites.DomainChange
	if err := decodeN(r, &c, 8<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.ChangeDomains(r.Context(), id, c)
	if errors.Is(err, sites.ErrNoChange) {
		writeJSON(w, 200, map[string]string{"job": ""})
		return
	}
	if err != nil {
		s.fail(w, r, "domains", id, err)
		return
	}
	s.note(r, "domains", id, "ok", domainDetail(c)+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func domainDetail(c sites.DomainChange) string {
	switch c.Op {
	case "add", "remove":
		return c.Op + " " + c.Name
	case "redirect-add":
		return fmt.Sprintf("redirect %s to %s (%d)", c.From, c.To, c.Code)
	case "redirect-remove":
		return "redirect remove " + c.From
	}
	return c.Op
}
