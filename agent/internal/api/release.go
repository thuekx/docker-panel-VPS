package api

// Static releases (SITE-03): publish a static website from a .zip, list the kept releases, put one back.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) staticReleases(w http.ResponseWriter, r *http.Request) {
	h, err := s.M.Releases(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]any{"releases": h, "keep": sites.KeepReleases, "max_bytes": sites.MaxRelease})
}

func (s *Server) staticPublish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !onlyKeys(w, r, "name") {
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeErr(w, 400, "content type must be application/octet-stream")
		return
	}
	if r.ContentLength > sites.MaxRelease {
		s.fail(w, r, "static-publish", id, sites.ErrTooBig)
		return
	}
	long(w, time.Hour)
	rel, err := s.M.PublishStatic(id, r.URL.Query().Get("name"), r.Body)
	if err != nil {
		s.fail(w, r, "static-publish", id, err)
		return
	}
	s.note(r, "static-publish", id, "ok", "release "+strconv.Itoa(rel.Release)+" "+rel.Name+" ("+strconv.Itoa(rel.Files)+" files)")
	writeJSON(w, 200, rel)
}

func (s *Server) staticRollback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n > 1<<30 {
		writeErr(w, 400, "bad release number")
		return
	}
	rel, err := s.M.RollbackStatic(id, n)
	if err != nil {
		s.fail(w, r, "static-rollback", id, err)
		return
	}
	s.note(r, "static-rollback", id, "ok", "release "+strconv.Itoa(n))
	writeJSON(w, 200, rel)
}
