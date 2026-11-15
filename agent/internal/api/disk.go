package api

// Disk usage (DISK-01): per website and for the server, and the clean-up of what no website uses.

import (
	"net/http"
	"time"
)

func (s *Server) siteDisk(w http.ResponseWriter, r *http.Request) {
	if !onlyKeys(w, r, "path") {
		return
	}
	long(w, time.Minute)
	d, err := s.M.Disk(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) serverDisk(w http.ResponseWriter, r *http.Request) {
	long(w, time.Minute)
	d, err := s.M.ServerDiskUsage(r.Context())
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) diskClean(w http.ResponseWriter, r *http.Request) {
	var c struct {
		What string `json:"what"`
	}
	if err := decodeN(r, &c, 1<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 6*time.Minute)
	freed, err := s.M.CleanDocker(r.Context(), c.What)
	if err != nil {
		s.fail(w, r, "disk-clean", c.What, err)
		return
	}
	s.note(r, "disk-clean", c.What, "ok", "freed "+freed)
	writeJSON(w, 200, map[string]string{"freed": freed})
}
