package api

// Own certificate routes (CERT-01). The private key goes in once and is never answered or written to the audit log.

import (
	"net/http"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) cert(w http.ResponseWriter, r *http.Request) {
	ci, err := s.M.Cert(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, ci)
}

func (s *Server) certSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := decodeN(r, &a, 2*sites.MaxCertPEM+4096); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ci, err := s.M.SetCert(id, []byte(a.Cert), []byte(a.Key))
	if err != nil {
		s.fail(w, r, "cert-set", id, err)
		return
	}
	s.note(r, "cert-set", id, "ok", strings.Join(ci.Names, ",")+" until "+time.Unix(ci.NotAfter, 0).UTC().Format("2006-01-02"))
	writeJSON(w, 200, ci)
}

func (s *Server) certRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.M.RemoveCert(id); err != nil {
		s.fail(w, r, "cert-remove", id, err)
		return
	}
	s.note(r, "cert-remove", id, "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
