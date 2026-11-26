package api

// ionCube routes (PHP-02): the loader card of a website and on/off as a job.

import "net/http"

func (s *Server) ionCube(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.IonCube(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) ionCubeSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		On bool `json:"on"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.StartIonCube(id, a.On)
	if err != nil {
		s.fail(w, r, "ioncube", id, err)
		return
	}
	detail := "off"
	if a.On {
		detail = "on"
	}
	s.note(r, "ioncube", id, "ok", detail+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
