package api

// Redis routes (REDIS-01): a website's own Redis, its figures, on/off/size as a job, and a flush.

import (
	"net/http"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) redis(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.RedisStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) redisSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		On   bool   `json:"on"`
		Mode string `json:"mode"`
		MB   int    `json:"mb"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var c *sites.RedisConf
	detail := "off"
	if a.On {
		c = &sites.RedisConf{Mode: a.Mode, MB: a.MB}
		detail = a.Mode
	}
	job, err := s.M.StartRedis(id, c)
	if err != nil {
		s.fail(w, r, "redis", id, err)
		return
	}
	s.note(r, "redis", id, "ok", detail+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func (s *Server) redisFlush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.M.FlushRedis(r.Context(), id); err != nil {
		s.fail(w, r, "redis-flush", id, err)
		return
	}
	s.note(r, "redis-flush", id, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
