package api

// DNS provider and wildcard certificate routes (CERT-01). The ClouDNS password goes in once; it is never answered
// or written to the audit log.

import (
	"net/http"
)

func (s *Server) dns(w http.ResponseWriter, r *http.Request) {
	c, err := s.M.DNS()
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) dnsSet(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Provider  string `json:"provider"`
		SubAuthID string `json:"sub_auth_id"`
		Password  string `json:"password"`
	}
	if err := decodeN(r, &a, 4096); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if a.Provider != "cloudns" {
		writeErr(w, 400, "provider must be cloudns")
		return
	}
	if err := s.M.SetDNS(r.Context(), a.SubAuthID, a.Password); err != nil {
		s.fail(w, r, "dns-set", "", err)
		return
	}
	s.note(r, "dns-set", "", "ok", "cloudns sub-user "+a.SubAuthID)
	c, _ := s.M.DNS()
	writeJSON(w, 200, c)
}

func (s *Server) dnsRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.M.RemoveDNS(r.Context()); err != nil {
		s.fail(w, r, "dns-remove", "", err)
		return
	}
	s.note(r, "dns-remove", "", "ok", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) wildcard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Zone string `json:"zone"`
	}
	if err := decodeN(r, &a, 1024); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	job, err := s.M.StartWildcard(r.Context(), id, a.Zone)
	if err != nil {
		s.fail(w, r, "wildcard", id, err)
		return
	}
	d := "off"
	if a.Zone != "" {
		d = "zone " + a.Zone
	}
	s.note(r, "wildcard", id, "ok", d+" job "+job)
	writeJSON(w, 202, map[string]string{"job": job})
}
