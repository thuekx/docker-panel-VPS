package web

// SFTP page (SFTP-01), like cPanel's FTP Accounts: SFTP logins of a website, each locked to the website's folder
// or a folder inside it, with a password and/or public keys. Passwords are hashed here (bcrypt) and only the hash
// goes to the agent; the audit log gets names, folders and key fingerprints.

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const sftpPassMin = 12 // SFTP is open to the internet: longer than a protected folder's password

type sftpData struct {
	Site agent.Site
	V    agent.SFTP
	Err  string
	Host string // the name to connect to: the one this panel is opened with
}

func (s *Server) sftpPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := sftpData{Site: site, Host: r.Host}
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		d.Host = h
	}
	v, err := s.ag.SFTP(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	s.render(w, r, "sftp", page{Title: "SFTP · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) sftpAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u, l := r.PostForm, userOf(r), s.lang(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/sftp/" + site.ID
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	c := agent.SFTPChange{Op: f.Get("op"), Name: strings.TrimSpace(f.Get("name"))}
	if c.Name == "" || len(c.Name) > 32 {
		back(s.tr.T(l, "sftp.name.need"))
		return
	}
	detail := site.ID + " " + c.Op + " " + c.Name
	hash := func() bool {
		pass := f.Get("password")
		if pass == "" && c.Op == "user-add" {
			return true // a key-only account
		}
		if len(pass) < sftpPassMin || len(pass) > authPassMax {
			back(s.tr.T(l, "sec.pass.len", sftpPassMin, authPassMax))
			return false
		}
		if pass != f.Get("password2") {
			back(s.tr.T(l, "sec.pass.same"))
			return false
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			back(err.Error())
			return false
		}
		c.Hash = string(h)
		return true
	}
	switch c.Op {
	case "user-add":
		c.Dir, c.Key = strings.TrimSpace(f.Get("dir")), strings.TrimSpace(f.Get("key"))
		if !hash() {
			return
		}
		if c.Hash == "" && c.Key == "" {
			back(s.tr.T(l, "sftp.auth.need"))
			return
		}
		detail += " dir " + c.Dir
	case "password-set":
		if f.Get("password") == "" {
			back(s.tr.T(l, "sftp.pass.need"))
			return
		}
		if !hash() {
			return
		}
	case "password-remove":
	case "user-remove":
		if f.Get("confirm") != "1" {
			back(s.tr.T(l, "sftp.confirm.need"))
			return
		}
	case "key-add":
		c.Key = strings.TrimSpace(f.Get("key"))
		if c.Key == "" || len(c.Key) > 16<<10 {
			back(s.tr.T(l, "sftp.key.need"))
			return
		}
	case "key-remove":
		c.Fingerprint = f.Get("fingerprint")
		detail += " " + c.Fingerprint
	case "dir-set":
		c.Dir = strings.TrimSpace(f.Get("dir"))
		detail += " " + c.Dir
	default:
		http.Error(w, "bad request", 400)
		return
	}
	changed, err := s.ag.ChangeSFTP(ctx, site.ID, c)
	if err != nil {
		s.audit1(u, r, "sftp", detail+" failed: "+err.Error())
		back(err.Error())
		return
	}
	if !changed {
		http.Redirect(w, r, base+"?done=same", http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "sftp", detail)
	http.Redirect(w, r, base+"?done="+c.Op+"#acc-"+c.Name, http.StatusSeeOther)
}
