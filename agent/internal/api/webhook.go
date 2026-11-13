package api

import (
	"errors"
	"net/http"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

// Auto-deploy webhook (APP-02c). The secret is returned only by webhook/secret; the audit log names the action only.

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	h, err := s.M.WebhookInfo(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) webhookSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sec, err := s.M.NewWebhookSecret(id)
	if err != nil {
		s.fail(w, r, "webhook-secret", id, err)
		return
	}
	s.note(r, "webhook-secret", id, "ok", "new secret")
	writeJSON(w, 200, map[string]string{"secret": sec})
}

func (s *Server) webhookRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.M.RemoveWebhook(id)
	s.result(w, r, "webhook-remove", id, err, map[string]any{"ok": true})
}

func (s *Server) webhookDeliver(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var h sites.Hook
	if err := decodeN(r, &h, sites.MaxWebhookBody*2); err != nil { // the body travels base64 inside JSON
		writeErr(w, 400, err.Error())
		return
	}
	d, err := s.M.Deliver(id, h)
	switch {
	case errors.Is(err, sites.ErrNoWebhook), errors.Is(err, sites.ErrNotFound):
		writeErr(w, 404, sites.ErrNoWebhook.Error())
		return
	case errors.Is(err, sites.ErrSignature):
		s.note(r, "webhook", id, "refused", "bad signature")
		writeErr(w, 401, err.Error())
		return
	case err != nil:
		s.fail(w, r, "webhook", id, err)
		return
	}
	detail := d.Event + " " + d.Result
	if d.Job != "" {
		detail += " job " + d.Job
	}
	if d.Detail != "" {
		detail += ": " + d.Detail
	}
	s.note(r, "webhook", id, "ok", detail)
	writeJSON(w, 200, d)
}
