package api

// Update routes (UPD-01). The passphrase of the optional server backup goes to the job only: never in the audit
// log or an answer.

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, 200, s.M.UpdateInfo(ctx, r.URL.Query().Get("fresh") == "1"))
}

func (s *Server) updateStart(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Tag        string `json:"tag"`
		Passphrase string `json:"passphrase"`
		Panel      string `json:"panel_version"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(a.Panel) > 40 || len(a.Tag) > 40 {
		writeErr(w, 400, "tag or panel_version is too long")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	job, err := s.M.StartUpdate(ctx, a.Tag, a.Passphrase, a.Panel, s.Version)
	if err != nil {
		s.fail(w, r, "update", "", err)
		return
	}
	detail := "to " + a.Tag + ", job " + job
	if a.Passphrase != "" {
		detail += ", server backup first"
	}
	s.note(r, "update", "", "ok", detail)
	writeJSON(w, 202, map[string]string{"job": job})
}
