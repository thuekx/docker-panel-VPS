package api

// Visitor statistics (OPS-01).

import (
	"net/http"
	"strconv"
)

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	v, err := s.M.Metrics(r.PathValue("id"), days)
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}
