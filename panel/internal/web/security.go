package web

// Security page (SEC-01): like cPanel's IP Blocker, Directory Privacy and Hotlink Protection. nostek-agent applies
// every rule at the edge as a job; the panel follows it. Passwords of protected folders are hashed here (bcrypt):
// only the hash goes to the agent, and nothing of it reaches the audit log.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const (
	authPassMin = 8
	authPassMax = 72 // bcrypt reads at most 72 bytes
)

type securityData struct {
	Site    agent.Site
	V       agent.Security
	Err     string
	Job     *agent.Job
	Steps   []stepRow
	Percent int
	Running bool
	YourIP  string
	Allow   string // allowed sites of hotlink protection, one per line
	Codes   []codeRow
	// wave 3
	Scanning bool   // a malware scan of this website runs (other changes stay possible)
	JobTitle string // the job card's title key
	Logins   []loginRow
	Since    string // when the login attempts started to be counted
	ScanAt   string // when the last scan finished
	ScanMin  int    // how long it took, in minutes (at least 1)
}

// loginRow is one address of the login attempts, with its last visit as text.
type loginRow struct {
	agent.LoginIP
	Last string
}

func clock(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}

// codeRow is one error code on the page: on or off, and its page in the File Manager.
type codeRow struct {
	Code int
	On   bool
	Path string
}

func (s *Server) securityPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := securityData{Site: site, YourIP: s.clientIP(r), JobTitle: "sec.job"}
	v, err := s.ag.Security(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	if v.Hotlink != nil {
		d.Allow = strings.Join(v.Hotlink.Allow, "\n")
	}
	for _, c := range v.ErrorCodes {
		row := codeRow{Code: c, Path: v.ErrorDir + "/" + strconv.Itoa(c) + ".html"}
		for _, x := range v.ErrorPages {
			row.On = row.On || x == c
		}
		d.Codes = append(d.Codes, row)
	}
	for _, x := range v.Logins.IPs {
		d.Logins = append(d.Logins, loginRow{LoginIP: x, Last: clock(x.LastSeen)})
	}
	d.Since = clock(v.Logins.Since)
	if l := v.Scan.Last; l != nil {
		d.ScanAt = clock(l.Finished)
		d.ScanMin = int((l.Finished-l.Started)/60) + 1
	}
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && j.Site == site.ID && (j.Kind == "security" || j.Kind == "malware-scan") {
			d.Job = &j
		}
	}
	if d.Job == nil && v.Job != nil && (v.Job.State == "running" || r.URL.Query().Get("job") != "") {
		d.Job = v.Job
	}
	if d.Job == nil && v.Scan.Job != nil && v.Scan.Job.State == "running" {
		d.Job = v.Scan.Job
	}
	d.Scanning = v.Scan.Job != nil && v.Scan.Job.State == "running"
	if d.Job != nil && d.Job.Kind == "malware-scan" {
		d.JobTitle = "sec.scan.job"
	}
	if d.Job != nil {
		d.Running = d.Job.State == "running" && d.Job.Kind == "security"
		d.Steps, d.Percent = jobProgress(*d.Job, agent.Site{})
	}
	s.render(w, r, "security", page{Title: s.tr.T(s.lang(r), "sec.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// splitList reads a list typed one per line or separated by commas or spaces.
func splitList(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}

// securityAction blocks or unblocks an address, protects a folder or changes its users, or turns hotlink
// protection on or off.
func (s *Server) securityAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u, l := r.PostForm, userOf(r), s.lang(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/security/" + site.ID
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	switch f.Get("op") {
	case "scan": // SEC-01 wave 3: malware scan of the website's files
		job, err := s.ag.StartScan(ctx, site.ID)
		if err != nil {
			s.audit1(u, r, "malware-scan", site.ID+" failed: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "malware-scan", site.ID)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
		return
	case "scan-trash": // a file the scan found goes to the website's file trash (restorable for 7 days)
		p := f.Get("path")
		if !strings.HasPrefix(p, "/") || len(p) > 4096 {
			http.Error(w, "bad request", 400)
			return
		}
		if f.Get("confirm") != "1" {
			back(s.tr.T(l, "sec.confirm.need"))
			return
		}
		if _, err := s.ag.FileOp(ctx, site.ID, "delete", p, "", ""); err != nil {
			s.audit1(u, r, "malware-trash", site.ID+" "+p+" failed: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "malware-trash", site.ID+" "+p)
		http.Redirect(w, r, base+"?done=trashed#scan", http.StatusSeeOther)
		return
	}
	c := agent.SecurityChange{Op: f.Get("op")}
	detail := site.ID + " " + c.Op
	switch c.Op {
	case "block-add", "block-remove":
		c.IP = strings.TrimSpace(f.Get("ip"))
		if c.IP == "" || len(c.IP) > 64 {
			back(s.tr.T(l, "sec.ip.need"))
			return
		}
		detail += " " + c.IP
	case "protect-set":
		c.Path, c.User = strings.TrimSpace(f.Get("path")), strings.TrimSpace(f.Get("user"))
		pass := f.Get("password")
		if c.Path == "" || c.User == "" || len(c.Path) > 200 || len(c.User) > 32 {
			back(s.tr.T(l, "sec.protect.need"))
			return
		}
		if len(pass) < authPassMin || len(pass) > authPassMax {
			back(s.tr.T(l, "sec.pass.len", authPassMin, authPassMax))
			return
		}
		if pass != f.Get("password2") {
			back(s.tr.T(l, "sec.pass.same"))
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			back(err.Error())
			return
		}
		c.Hash = string(h)
		detail += " " + c.Path + " user " + c.User
	case "protect-user-remove":
		c.Path, c.User = f.Get("path"), f.Get("user")
		detail += " " + c.Path + " user " + c.User
	case "protect-remove":
		c.Path = f.Get("path")
		if f.Get("confirm") != "1" {
			back(s.tr.T(l, "sec.confirm.need"))
			return
		}
		detail += " " + c.Path
	case "hotlink-on":
		c.Allow = splitList(f.Get("allow"))
		if len(c.Allow) > 20 {
			back(s.tr.T(l, "sec.hot.many"))
			return
		}
		detail += " allow " + strings.Join(c.Allow, ",")
	case "hotlink-off":
	case "errpage-on", "errpage-off":
		c.Code, _ = strconv.Atoi(f.Get("code"))
		detail += " " + strconv.Itoa(c.Code)
	case "mime-add":
		c.Type, c.Exts = strings.TrimSpace(f.Get("type")), strings.TrimSpace(f.Get("exts"))
		if c.Type == "" || c.Exts == "" || len(c.Type) > 200 || len(c.Exts) > 400 {
			back(s.tr.T(l, "sec.mime.need"))
			return
		}
		detail += " " + c.Type + " " + c.Exts
	case "mime-remove":
		c.Type = f.Get("type")
		detail += " " + c.Type
	default:
		http.Error(w, "bad request", 400)
		return
	}
	job, err := s.ag.ChangeSecurity(ctx, site.ID, c)
	if err != nil {
		s.audit1(u, r, "security", detail+" failed: "+err.Error())
		back(err.Error())
		return
	}
	if job == "" {
		http.Redirect(w, r, base+"?done=nochange", http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "security", detail)
	http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
}
