package web

// Domains page (DOM-01): every name of a website with where DNS points it and its certificate, extra names added
// or removed, "check DNS again" that serves the names now pointing here, and redirects per path. nostek-agent makes
// every change as a job; the panel follows it.

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type domainRow struct {
	agent.DomainCheck
	Main      bool   // the website's main address
	CanRemove bool   // extra names only
	Why       string // locale key that says what to do with the name
	Until     string // certificate end, for people
	Days      int
}

type domainsData struct {
	Site     agent.Site
	D        agent.Domains
	Rows     []domainRow
	Err      string
	Job      *agent.Job
	Steps    []stepRow
	Percent  int
	Running  bool
	Ready    int // names that point here but are not served yet
	Aliases  int
	Cert     agent.CertInfo // CERT-01: the website's own certificate
	CertEnd  string
	CertDays int
	Req      agent.CertRequest // CERT-02: a waiting certificate request
	ReqAt    string
	OwnNames []string // the website's names a request or a self-signed certificate may cover
}

// domainWhy says, as a locale key, what a name needs.
func domainWhy(h agent.DomainCheck) string {
	switch {
	case h.Routed && h.DNS == "here":
		return "dom.why.ok"
	case h.DNS == "mixed":
		return "dom.why.mixed"
	case h.Routed && h.Role == "preview":
		return "dom.why.preview"
	case h.Routed:
		return "dom.why.moved"
	case h.DNS == "here":
		return "dom.why.ready"
	case h.DNS == "elsewhere":
		return "dom.why.elsewhere"
	case h.DNS == "none":
		return "dom.why.none"
	}
	return "dom.why.error"
}

func (s *Server) domainsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := domainsData{Site: site}
	dm, err := s.ag.Domains(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.D = dm
	for _, h := range dm.Hosts {
		row := domainRow{DomainCheck: h, Main: h.Name == dm.Canonical, CanRemove: h.Role == "alias" || h.Role == "www", Why: domainWhy(h)}
		if h.CertExpires > 0 {
			t := time.Unix(h.CertExpires, 0)
			row.Until, row.Days = t.In(s.loc()).Format("02 Jan 2006"), int(time.Until(t).Hours()/24)
		}
		if !h.Routed && h.DNS == "here" {
			d.Ready++
		}
		if h.Role == "alias" {
			d.Aliases++
		}
		d.Rows = append(d.Rows, row)
	}
	if ci, err := s.ag.Cert(ctx, site.ID); err == nil {
		d.Cert = ci
		if ci.NotAfter > 0 {
			t := time.Unix(ci.NotAfter, 0)
			d.CertEnd, d.CertDays = t.In(s.loc()).Format("02 Jan 2006"), int(time.Until(t).Hours()/24)
		}
	}
	if rq, err := s.ag.CertRequest(ctx, site.ID); err == nil && rq.CSR != "" {
		d.Req, d.ReqAt = rq, time.Unix(rq.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04")
	}
	for _, h := range dm.Hosts {
		if h.Role != "preview" {
			d.OwnNames = append(d.OwnNames, h.Name)
		}
	}
	if base := strings.TrimPrefix(dm.Canonical, "www."); slices.Contains(d.OwnNames, base) {
		d.OwnNames = append(d.OwnNames, "*."+base) // a wildcard of the main domain
	}
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && j.Site == site.ID && j.Kind == "domains" {
			d.Job = &j
		}
	}
	if d.Job == nil && dm.Job != nil && (dm.Job.State == "running" || r.URL.Query().Get("job") != "") {
		d.Job = dm.Job
	}
	if d.Job != nil {
		d.Running = d.Job.State == "running"
		d.Steps, d.Percent = jobProgress(*d.Job, agent.Site{})
	}
	s.render(w, r, "domains", page{Title: s.tr.T(s.lang(r), "dom.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// domainsAction adds or removes a name, checks DNS again, or adds or removes a redirect.
func (s *Server) domainsAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u, l := r.PostForm, userOf(r), s.lang(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/domains/" + site.ID
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	c := agent.DomainChange{Op: f.Get("op")}
	detail := site.ID + " " + c.Op
	switch c.Op {
	case "add":
		c.Name = strings.ToLower(strings.TrimSpace(f.Get("name")))
		if c.Name == "" || len(c.Name) > 253 {
			back(s.tr.T(l, "dom.name.need"))
			return
		}
		detail += " " + c.Name
	case "remove":
		c.Name = f.Get("name")
		if f.Get("confirm") != "1" {
			back(s.tr.T(l, "dom.confirm.need"))
			return
		}
		detail += " " + c.Name
	case "refresh":
	case "redirect-add":
		c.From, c.To = strings.TrimSpace(f.Get("from")), strings.TrimSpace(f.Get("to"))
		c.Code, _ = strconv.Atoi(f.Get("code"))
		c.Prefix = f.Get("prefix") == "1"
		if c.From == "" || c.To == "" || len(c.From) > 512 || len(c.To) > 1024 {
			back(s.tr.T(l, "dom.redirect.need"))
			return
		}
		detail += " " + c.From + " to " + c.To + " (" + strconv.Itoa(c.Code) + ")"
		if c.Prefix {
			detail += " folder"
		}
	case "redirect-remove":
		c.From = f.Get("from")
		detail += " " + c.From
	default:
		http.Error(w, "bad request", 400)
		return
	}
	job, err := s.ag.ChangeDomains(ctx, site.ID, c)
	if err != nil {
		s.audit1(u, r, "domains", detail+" failed: "+err.Error())
		back(err.Error())
		return
	}
	if job == "" {
		http.Redirect(w, r, base+"?done=nochange", http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "domains", detail)
	http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
}

// certAction stores or removes a website's own certificate (CERT-01). The key is passed to the agent once and is
// never logged; the audit names the certificate's names and end date.
func (s *Server) certAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f, u, l := r.PostForm, userOf(r), s.lang(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/domains/" + site.ID
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg)+"#cert", http.StatusSeeOther) }
	switch f.Get("op") {
	case "set":
		cert, key := strings.TrimSpace(f.Get("cert")), strings.TrimSpace(f.Get("key"))
		if cert == "" {
			back(s.tr.T(l, "cert.need"))
			return
		}
		if len(cert) > 64<<10 || len(key) > 64<<10 {
			back(s.tr.T(l, "cert.toobig"))
			return
		}
		if key != "" {
			key += "\n"
		} // CERT-02: no key = the certificate of the waiting request, paired with the key on the server
		ci, err := s.ag.SetCert(ctx, site.ID, cert+"\n", key)
		if err != nil {
			s.audit1(u, r, "cert.set", site.ID+" refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.set", site.ID+" "+strings.Join(ci.Names, ",")+" until "+time.Unix(ci.NotAfter, 0).UTC().Format("2006-01-02"))
		http.Redirect(w, r, base+"?done=cert#cert", http.StatusSeeOther)
	case "wildcard":
		// CERT-01: zone "" turns the wildcard off; the agent checks the zone against the website and ClouDNS
		zone := strings.ToLower(strings.TrimSpace(f.Get("zone")))
		if len(zone) > 253 {
			http.Error(w, "bad request", 400)
			return
		}
		job, err := s.ag.StartWildcard(ctx, site.ID, zone)
		detail := site.ID + " off"
		if zone != "" {
			detail = site.ID + " zone " + zone
		}
		if err != nil {
			s.audit1(u, r, "cert.wildcard", detail+" refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.wildcard", detail)
		http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
	case "csr":
		in := agent.CSRInput{Names: f["names"], KeyType: f.Get("key"), Org: strings.TrimSpace(f.Get("org")), Unit: strings.TrimSpace(f.Get("unit")),
			City: strings.TrimSpace(f.Get("city")), State: strings.TrimSpace(f.Get("state")), Country: strings.TrimSpace(f.Get("country"))}
		if len(in.Names) == 0 || len(in.Names) > 20 {
			back(s.tr.T(l, "csr.need"))
			return
		}
		rq, err := s.ag.CreateCSR(ctx, site.ID, in)
		if err != nil {
			s.audit1(u, r, "cert.csr", site.ID+" refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.csr", site.ID+" "+strings.Join(rq.Names, ",")+" "+rq.KeyType)
		http.Redirect(w, r, base+"?done=csr#csr", http.StatusSeeOther)
	case "csr-delete":
		if err := s.ag.DeleteCSR(ctx, site.ID); err != nil {
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.csr-delete", site.ID)
		http.Redirect(w, r, base+"?done=csrdeleted#cert", http.StatusSeeOther)
	case "self-signed":
		days, _ := strconv.Atoi(f.Get("days"))
		if len(f["names"]) == 0 || len(f["names"]) > 20 || days < 1 {
			back(s.tr.T(l, "csr.need"))
			return
		}
		ci, err := s.ag.SelfSigned(ctx, site.ID, f["names"], f.Get("key"), days)
		if err != nil {
			s.audit1(u, r, "cert.self-signed", site.ID+" refused: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.self-signed", site.ID+" "+strings.Join(ci.Names, ",")+" until "+time.Unix(ci.NotAfter, 0).UTC().Format("2006-01-02"))
		http.Redirect(w, r, base+"?done=selfsigned#cert", http.StatusSeeOther)
	case "remove":
		if err := s.ag.RemoveCert(ctx, site.ID); err != nil {
			back(err.Error())
			return
		}
		s.audit1(u, r, "cert.remove", site.ID)
		http.Redirect(w, r, base+"?done=certremoved#cert", http.StatusSeeOther)
	default:
		http.Error(w, "bad request", 400)
	}
}
