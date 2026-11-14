package api

// Update image (IMG-01). The audit log names the job only.

import "net/http"

func (s *Server) images(w http.ResponseWriter, r *http.Request) {
	img, err := s.M.ImageStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, img)
}

func (s *Server) imageUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.M.StartImageUpdate(id)
	if err != nil {
		s.fail(w, r, "image-update", id, err)
		return
	}
	s.note(r, "image-update", id, "ok", "job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
