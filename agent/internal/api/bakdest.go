package api

// Backup plan routes (BAK-03): the schedule of server backups and their destinations. The passphrase only turns
// into a public key and is never written; destination secrets never come back in an answer or the audit log.

import (
	"net/http"
	"strconv"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) backupPlan(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.BackupPlanView()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) serverBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Every      string `json:"every"`
		Hour       int    `json:"hour"`
		Weekday    int    `json:"weekday"`
		Passphrase string `json:"passphrase"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.M.SetBackupSchedule(a.Every, a.Hour, a.Weekday, a.Passphrase); err != nil {
		s.fail(w, r, "backup-schedule", "", err)
		return
	}
	detail := "off"
	if a.Every != "" {
		detail = a.Every + " " + strconv.Itoa(a.Hour) + ":00"
	}
	if a.Passphrase != "" {
		detail += " new key"
	}
	s.note(r, "backup-schedule", "", "ok", detail)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Panel string `json:"panel_version"`
	}
	if err := decode(r, &a); err != nil || len(a.Panel) > 40 {
		writeErr(w, 400, "bad request")
		return
	}
	job, err := s.M.StartScheduledBackup(a.Panel, s.Version)
	if err != nil {
		s.fail(w, r, "server-backup-scheduled", "", err)
		return
	}
	s.note(r, "server-backup-scheduled", "", "ok", "job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}

func (s *Server) backupDestSave(w http.ResponseWriter, r *http.Request) {
	var in sites.DestInput
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id, err := s.M.SaveDest(in)
	if err != nil {
		s.fail(w, r, "backup-dest", in.ID, err)
		return
	}
	s.note(r, "backup-dest", id, "ok", in.Kind+" "+in.Name)
	writeJSON(w, 200, map[string]string{"id": id})
}

func (s *Server) backupDestDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("did")
	if err := s.M.DeleteDest(id); err != nil {
		s.fail(w, r, "backup-dest-delete", id, err)
		return
	}
	s.note(r, "backup-dest-delete", id, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) backupDestTest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("did")
	res, err := s.M.TestDest(r.Context(), id)
	if err != nil {
		s.fail(w, r, "backup-dest-test", id, err)
		return
	}
	out := "ok"
	if !res.OK {
		out = "failed"
	}
	s.note(r, "backup-dest-test", id, out, "")
	writeJSON(w, 200, res)
}

func (s *Server) backupSSHKey(w http.ResponseWriter, r *http.Request) {
	k, err := s.M.SSHKey(r.Context())
	if err != nil {
		s.fail(w, r, "backup-ssh-key", "", err)
		return
	}
	writeJSON(w, 200, map[string]string{"key": k})
}

func (s *Server) serverBackupSend(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Dest string `json:"dest"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name := r.PathValue("name")
	job, err := s.M.StartServerUpload(name, a.Dest)
	if err != nil {
		s.fail(w, r, "server-backup-send", a.Dest, err)
		return
	}
	s.note(r, "server-backup-send", a.Dest, "ok", name+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
