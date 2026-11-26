package api

// Server backup routes (BAK-02). The passphrase is passed to the job and never written anywhere: not in the audit
// log, not in an answer. A restore is not offered here: it runs from the installer on the new server.

import (
	"net/http"
	"time"
)

func (s *Server) serverBackups(w http.ResponseWriter, r *http.Request) {
	b, err := s.M.ListServerBackups()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) serverBackupStart(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Passphrase string `json:"passphrase"`
		Panel      string `json:"panel_version"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(a.Panel) > 40 {
		writeErr(w, 400, "panel_version is too long")
		return
	}
	job, err := s.M.StartServerBackup(a.Passphrase, a.Panel, s.Version)
	if err != nil {
		s.fail(w, r, "server-backup", "", err)
		return
	}
	s.note(r, "server-backup", "", "ok", "job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func (s *Server) serverBackupGet(w http.ResponseWriter, r *http.Request) {
	f, fi, err := s.M.OpenServerBackup(r.PathValue("name"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	defer f.Close()
	long(w, 6*time.Hour)
	s.note(r, "server-backup-download", "", "ok", fi.Name())
	download(w, fi.Name(), "application/octet-stream", fi.Size(), f)
}

func (s *Server) serverBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.M.DeleteServerBackup(name); err != nil {
		s.fail(w, r, "server-backup-delete", "", err)
		return
	}
	s.note(r, "server-backup-delete", "", "ok", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
