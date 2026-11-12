package api

// Site backup routes (BAK-01). The audit log names the backup file and what was restored, never its content.

import (
	"net/http"
	"strconv"
	"time"
)

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	b, err := s.M.ListBackups(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) backupStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.M.StartBackup(id)
	if err != nil {
		s.fail(w, r, "backup", id, err)
		return
	}
	s.note(r, "backup", id, "ok", "job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func (s *Server) backupGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, fi, err := s.M.OpenBackup(id, r.PathValue("name"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	defer f.Close()
	long(w, 3*time.Hour)
	s.note(r, "backup-download", id, "ok", fi.Name())
	download(w, id+"-"+fi.Name(), "application/gzip", fi.Size(), f)
}

func (s *Server) backupDelete(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if err := s.M.DeleteBackup(id, name); err != nil {
		s.fail(w, r, "backup-delete", id, err)
		return
	}
	s.note(r, "backup-delete", id, "ok", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) backupSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Daily bool `json:"daily"`
		Keep  int  `json:"keep"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.M.SetSchedule(id, a.Daily, a.Keep); err != nil {
		s.fail(w, r, "backup-schedule", id, err)
		return
	}
	on := "off"
	if a.Daily {
		on = "on"
	}
	s.note(r, "backup-schedule", id, "ok", "daily "+on+" keep "+strconv.Itoa(a.Keep))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	var a struct {
		What string `json:"what"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.StartRestore(id, name, a.What)
	if err != nil {
		s.fail(w, r, "backup-restore", id, err)
		return
	}
	s.note(r, "backup-restore", id, "ok", name+" "+a.What+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
