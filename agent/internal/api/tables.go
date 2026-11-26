package api

// Check, repair and optimize of a website's tables (DB-02). Table names are not secret; the audit log records the
// operation, how many tables and whether the database reported an error.

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) dbTables(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var q struct {
		Op     string   `json:"op"`
		Tables []string `json:"tables"`
	}
	if err := decodeN(r, &q, 128<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 6*time.Minute)
	a, err := s.M.Tables(r.Context(), id, q.Op, q.Tables)
	if err != nil {
		s.fail(w, r, "db-tables", id, err)
		return
	}
	res := "ok"
	if !a.OK {
		res = "errors reported"
	}
	n := len(q.Tables)
	what := fmt.Sprintf("%d tables", n)
	if n == 0 {
		what = "all tables"
	}
	s.note(r, "db-tables", id, "ok", fmt.Sprintf("%s %s: %s", q.Op, what, res))
	writeJSON(w, 200, a)
}

func (s *Server) remoteDB(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.RemoteDB(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) remoteDBSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var q struct {
		Allow []string `json:"allow"`
	}
	if err := decodeN(r, &q, 8<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.SetRemoteDB(r.Context(), id, q.Allow)
	if err != nil {
		s.fail(w, r, "db-remote", id, err)
		return
	}
	d := "off"
	if len(q.Allow) > 0 {
		d = "allow " + strings.Join(q.Allow, ",")
	}
	s.note(r, "db-remote", id, "ok", d+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
