package api

// WordPress migration routes (MIG-01). The audit log names the way and the old host only: never the migration code,
// its key or the download link's query.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) migrateStatus(w http.ResponseWriter, r *http.Request) {
	f, err := s.M.MigrateStatus(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, f)
}

func (s *Server) migrateUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !onlyKeys(w, r, "part") {
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeErr(w, 400, "content type must be application/octet-stream")
		return
	}
	if r.ContentLength > sites.MaxMigrate {
		s.fail(w, r, "migrate-upload", id, sites.ErrTooBig)
		return
	}
	long(w, 6*time.Hour)
	part := r.URL.Query().Get("part")
	n, err := s.M.MigrateUpload(id, part, r.Body)
	if err != nil {
		s.fail(w, r, "migrate-upload", id, err)
		return
	}
	s.note(r, "migrate-upload", id, "ok", part+" "+strconv.FormatInt(n>>20, 10)+" MB")
	writeJSON(w, 200, map[string]int64{"bytes": n})
}

func (s *Server) migrateClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.M.MigrateClear(id); err != nil {
		s.fail(w, r, "migrate-clear", id, err)
		return
	}
	s.note(r, "migrate-clear", id, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) migrateStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a sites.MigrateRequest
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.StartMigrate(id, a)
	if err != nil {
		s.fail(w, r, "migrate", id, err)
		return
	}
	s.note(r, "migrate", id, "ok", migrateDetail(a)+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

// migrateDetail says how the site comes in, naming only the old host.
func migrateDetail(a sites.MigrateRequest) string {
	switch a.Way {
	case "plugin":
		if c, err := sites.ParseMigrateCode(a.Code); err == nil {
			return "plugin from " + c.Home.Hostname()
		}
		return "plugin"
	case "backup":
		d := "backup upload"
		if u, err := url.Parse(strings.TrimSpace(a.Link)); err == nil && a.Link != "" {
			d = "backup link " + u.Hostname()
		}
		if a.Root != "" {
			d += " folder " + a.Root
		}
		return d
	}
	return a.Way
}
