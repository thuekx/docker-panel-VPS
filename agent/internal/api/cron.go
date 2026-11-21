package api

// Cron jobs (OPS-01). The audit log names the job and its schedule, never the command (it may carry a token).

import (
	"errors"
	"net/http"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) cron(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Cron(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) cronChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c sites.CronChange
	if err := decodeN(r, &c, 8<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.ChangeCron(id, c)
	if errors.Is(err, sites.ErrNoChange) {
		writeJSON(w, 200, map[string]any{"id": job, "changed": false})
		return
	}
	if err != nil {
		s.fail(w, r, "cron", id, err)
		return
	}
	detail := c.Op + " " + job
	if c.Op == "add" || c.Op == "edit" {
		detail += " [" + c.Schedule + "]"
	}
	s.note(r, "cron", id, "ok", detail)
	writeJSON(w, 200, map[string]any{"id": job, "changed": true})
}
