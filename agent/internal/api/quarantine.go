package api

// SEC-02 routes: a website's scan schedule and the quarantine.

import "net/http"

func (s *Server) scanSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Every      string `json:"every"`
		Quarantine bool   `json:"quarantine"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	site, err := s.M.SetScanSchedule(id, a.Every, a.Quarantine)
	if err != nil {
		s.fail(w, r, "scan-schedule", id, err)
		return
	}
	detail := "off"
	if a.Every != "" {
		detail = a.Every
		if a.Quarantine {
			detail += ", quarantine"
		}
	}
	s.note(r, "scan-schedule", id, "ok", detail)
	writeJSON(w, 200, site)
}

func (s *Server) quarantineAdd(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Path      string `json:"path"`
		Signature string `json:"signature"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	q, err := s.M.QuarantineFile(id, a.Path, a.Signature, false)
	if err != nil {
		s.fail(w, r, "quarantine", id, err)
		return
	}
	s.note(r, "quarantine", id, "ok", q.ID+" "+q.Path)
	writeJSON(w, 200, q)
}

func (s *Server) quarantineList(w http.ResponseWriter, r *http.Request) {
	list, err := s.M.Quarantine()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]any{"files": list})
}

func (s *Server) quarantineRestore(w http.ResponseWriter, r *http.Request) {
	q, err := s.M.QuarantineRestore(r.PathValue("qid"))
	if err != nil {
		s.fail(w, r, "quarantine-restore", q.Site, err)
		return
	}
	s.note(r, "quarantine-restore", q.Site, "ok", q.ID+" "+q.Path)
	writeJSON(w, 200, q)
}

func (s *Server) quarantineDelete(w http.ResponseWriter, r *http.Request) {
	q, err := s.M.QuarantineDelete(r.PathValue("qid"))
	if err != nil {
		s.fail(w, r, "quarantine-delete", q.Site, err)
		return
	}
	s.note(r, "quarantine-delete", q.Site, "ok", q.ID+" "+q.Path)
	writeJSON(w, 200, q)
}
