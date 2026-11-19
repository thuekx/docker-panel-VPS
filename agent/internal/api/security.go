package api

// Security routes (SEC-01): a website's blocked addresses, protected folders and hotlink protection. The password
// hash of a protected folder is never written to the audit log; addresses, folders and user names are.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) security(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Security(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) securityChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c sites.SecurityChange
	if err := decodeN(r, &c, 8<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.ChangeSecurity(id, c)
	if errors.Is(err, sites.ErrNoChange) {
		writeJSON(w, 200, map[string]string{"job": ""})
		return
	}
	if err != nil {
		s.fail(w, r, "security", id, err)
		return
	}
	s.note(r, "security", id, "ok", securityDetail(c)+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

// securityDetail is the audit line of a change; never the hash.
func securityDetail(c sites.SecurityChange) string {
	switch c.Op {
	case "block-add", "block-remove":
		return c.Op + " " + c.IP
	case "protect-set", "protect-user-remove":
		return c.Op + " " + c.Path + " user " + c.User
	case "protect-remove":
		return c.Op + " " + c.Path
	case "hotlink-on":
		return c.Op + " allow " + strings.Join(c.Allow, ",")
	}
	return c.Op
}
