package api

// SFTP routes (SFTP-01): a website's SFTP accounts. The password hash is never written to the audit log or sent
// back; names, folders and key fingerprints are.

import (
	"errors"
	"net/http"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) sftp(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.SFTP(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) sftpChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c sites.SFTPChange
	if err := decodeN(r, &c, 32<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	err := s.M.ChangeSFTP(id, c)
	if errors.Is(err, sites.ErrNoChange) {
		writeJSON(w, 200, map[string]bool{"changed": false})
		return
	}
	if err != nil {
		s.fail(w, r, "sftp", id, err)
		return
	}
	s.note(r, "sftp", id, "ok", sftpDetail(c))
	writeJSON(w, 200, map[string]bool{"changed": true})
}

// sftpDetail is the audit line of a change; never the hash, never the whole key.
func sftpDetail(c sites.SFTPChange) string {
	d := c.Op + " " + c.Name
	switch c.Op {
	case "user-add":
		d += " dir " + c.Dir
		if c.Hash != "" {
			d += " password"
		}
		if c.Key != "" {
			d += " key"
		}
	case "dir-set":
		d += " " + c.Dir
	case "key-remove":
		d += " " + c.Fingerprint
	}
	return d
}
