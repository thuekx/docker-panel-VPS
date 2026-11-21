package api

// Change PHP version (PHP-01). The audit log names the versions and the job.

import "net/http"

func (s *Server) perf(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Perf(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) phpChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c struct {
		PHP string `json:"php"`
	}
	if err := decodeN(r, &c, 1<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.StartPHPChange(id, c.PHP)
	if err != nil {
		s.fail(w, r, "php-change", id, err)
		return
	}
	s.note(r, "php-change", id, "ok", "PHP "+c.PHP+", job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
