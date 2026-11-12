package api

import (
	"net/http"
	"time"
)

// artisan runs one artisan command from the agent's fixed list on a Laravel website (APP-01 wave 2).
func (s *Server) artisan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Op string `json:"op"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 6*time.Minute)
	out, err := s.M.Artisan(r.Context(), id, a.Op)
	if err != nil {
		s.fail(w, r, "artisan", id, err)
		return
	}
	s.note(r, "artisan", id, "ok", a.Op)
	writeJSON(w, 200, map[string]string{"output": out})
}
