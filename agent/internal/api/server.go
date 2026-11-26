package api

// Server page routes (OPS-02): core services with restart, the top processes, and the drift of managed files.

import (
	"net/http"
	"regexp"
)

var coreNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)

func (s *Server) coreServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"services": s.M.CoreServices(r.Context())})
}

func (s *Server) coreRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !coreNameRe.MatchString(name) {
		writeErr(w, 400, "bad service name")
		return
	}
	if err := s.M.RestartCore(r.Context(), name); err != nil {
		s.fail(w, r, "core-restart", name, err)
		return
	}
	s.note(r, "core-restart", name, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Processes(r.Context())
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) drift(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.DriftCheck()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]any{"sites": v})
}

func (s *Server) driftFix(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.M.DriftFix(r.Context(), id); err != nil {
		s.fail(w, r, "drift-fix", id, err)
		return
	}
	s.note(r, "drift-fix", id, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
