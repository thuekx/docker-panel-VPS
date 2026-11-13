package web

// Auto-deploy webhook (APP-02c). POST /hooks/{id} is the one public route without a session: the Git host calls it
// on every push. The panel only passes the raw body and the signature headers on to nostek-agent, which alone
// keeps the website's secret and decides; nothing here trusts the delivery.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const maxHookBody = 1 << 20 // the agent refuses larger deliveries too

func hookAnswer(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) hookDeliver(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) {
		hookAnswer(w, 404, map[string]string{"error": "not found"})
		return
	}
	if !s.hookLimit.Allow(s.clientIP(r)) {
		hookAnswer(w, 429, map[string]string{"error": "too many deliveries; try again in a minute"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		hookAnswer(w, 413, map[string]string{"error": "delivery too large"})
		return
	}
	h := agent.Hook{Signature: r.Header.Get("X-Hub-Signature-256"), Token: r.Header.Get("X-Gitlab-Token"), Body: body}
	for _, k := range []string{"X-GitHub-Event", "X-Gitea-Event", "X-Gogs-Event", "X-Gitlab-Event"} {
		if v := r.Header.Get(k); v != "" {
			h.Event = v
			break
		}
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d, err := s.ag.Deliver(ctx, id, h)
	var ae *agent.Error
	switch {
	case err == nil:
		hookAnswer(w, 200, map[string]string{"result": d.Result, "detail": d.Detail, "job": d.Job})
	case errors.As(err, &ae) && ae.Code == 401:
		hookAnswer(w, 401, map[string]string{"error": "signature does not match this website's secret"})
	case errors.As(err, &ae) && (ae.Code == 404 || ae.Code == 413 || ae.Code == 400):
		hookAnswer(w, ae.Code, map[string]string{"error": ae.Msg})
	default:
		hookAnswer(w, 502, map[string]string{"error": "the server could not take the delivery now"})
	}
}

// hookRow is one delivery as the Overview shows it.
type hookRow struct {
	agent.Delivery
	When  string
	Short string
}

type hookView struct {
	agent.Webhook
	URL    string
	Secret string // only right after it was made
	Rows   []hookRow
}

func (s *Server) hookView(r *http.Request, wh agent.Webhook, id, secret string) *hookView {
	v := &hookView{Webhook: wh, URL: "https://" + r.Host + "/hooks/" + id, Secret: secret}
	for _, d := range wh.Deliveries {
		row := hookRow{Delivery: d, When: time.Unix(d.At, 0).In(s.loc()).Format("02 Jan 2006 15:04:05")}
		if len(d.Commit) >= 7 {
			row.Short = d.Commit[:7]
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}
