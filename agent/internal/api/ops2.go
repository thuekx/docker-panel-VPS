package api

// Watchdog events and resource usage (OPS-01 wave 2).

import (
	"net/http"
	"strconv"
)

func (s *Server) watchdog(w http.ResponseWriter, r *http.Request) {
	if !onlyKeys(w, r, "since") {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if since < 0 {
		since = 0
	}
	writeJSON(w, 200, s.M.Watchdog(since))
}

func (s *Server) siteUsage(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Usage(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) serverUsage(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Usage("")
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}
