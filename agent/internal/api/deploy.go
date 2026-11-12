package api

// Deploy routes of a Node.js website (APP-02): history, a new deploy (a job), a build log, a rollback.

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

func release(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > 1_000_000 {
		return 0, errors.New("release must be a positive number")
	}
	return n, nil
}

func (s *Server) deploys(w http.ResponseWriter, r *http.Request) {
	h, err := s.M.History(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) deployStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.M.StartDeploy(id)
	if err != nil {
		s.fail(w, r, "deploy", id, err)
		return
	}
	s.note(r, "deploy", id, "ok", "job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func (s *Server) deployLog(w http.ResponseWriter, r *http.Request) {
	n, err := release(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	l, err := s.M.BuildLog(r.PathValue("id"), n)
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]string{"log": l})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := release(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 5*time.Minute)
	if err := s.M.Rollback(r.Context(), id, n); err != nil {
		s.fail(w, r, "rollback", id, err)
		return
	}
	s.note(r, "rollback", id, "ok", "release "+strconv.Itoa(n))
	writeJSON(w, 200, map[string]any{"ok": true, "release": n})
}
