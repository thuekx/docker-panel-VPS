package api

// Check, repair and optimize of a website's tables (DB-02). Table names are not secret; the audit log records the
// operation, how many tables and whether the database reported an error.

import (
	"fmt"
	"net/http"
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
