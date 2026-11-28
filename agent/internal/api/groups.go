package api

// GRP-01: website groups (shared CPU, memory and process limits).

import (
	"fmt"
	"net/http"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	g, err := s.M.Groups()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]any{"groups": g})
}

func (s *Server) groupSet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var l sites.GroupLimits
	if err := decodeN(r, &l, 4<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.M.SetGroup(r.Context(), name, l); err != nil {
		s.fail(w, r, "group.set", name, err)
		return
	}
	s.note(r, "group.set", name, "ok", fmt.Sprintf("cpu %d%%, memory %d MB, tasks %d", l.CPU, l.Memory, l.Tasks))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) groupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.M.DeleteGroup(r.Context(), name); err != nil {
		s.fail(w, r, "group.delete", name, err)
		return
	}
	s.note(r, "group.delete", name, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) siteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Group string `json:"group"`
	}
	if err := decodeN(r, &in, 1<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 6*time.Minute)
	if err := s.M.SetSiteGroup(r.Context(), id, in.Group); err != nil {
		s.fail(w, r, "site.group", id, err)
		return
	}
	what := in.Group
	if what == "" {
		what = "none"
	}
	s.note(r, "site.group", id, "ok", "group "+what)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
