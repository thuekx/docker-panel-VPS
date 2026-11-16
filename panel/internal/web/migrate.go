package web

// Migrate page (MIG-01): bring an existing WordPress site into a WordPress website of the panel, through the
// Nostek Migrate plugin on the old site, or from a cPanel backup (uploaded or fetched from a link). nostek-agent does
// the work; the panel streams uploads through, hands out the plugin zip and follows the job. The audit log names
// the way and the old host only, never the migration code.

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const (
	maxMigrateBody = 40<<30 + 1<<20 // a backup and its database, up to 20 GB each, plus form overhead
	migratePlugin  = "plugin/nostek-migrate.php"
)

var migrateSteps = []string{"inspect", "safety database", "safety files", "copy files", "copy database", "load database", "switch files", "addresses", "tidy", "check"}

type migrateData struct {
	Site     agent.Site
	F        agent.MigrateFiles
	Archive  string // uploaded sizes, for people
	Database string
	Err      string
	Job      *agent.Job
	Steps    []stepRow
	Percent  int
	Result   string   // the summary of a finished migration
	Choose   []string // the WordPress folders a backup holds, when the job asks to pick one
	Running  bool
}

func (s *Server) migratePage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	if !site.IsWP() {
		s.notFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := migrateData{Site: site}
	f, err := s.ag.MigrateStatus(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.F = f
	if f.Archive > 0 {
		d.Archive = size(f.Archive)
	}
	if f.Database > 0 {
		d.Database = size(f.Database)
	}
	if id := r.URL.Query().Get("job"); jobRe.MatchString(id) {
		if j, err := s.ag.Job(ctx, id); err == nil && j.Site == site.ID && j.Kind == "migrate" {
			d.Job = &j
		}
	}
	if d.Job == nil && f.Job != nil && (f.Job.State == "running" || r.URL.Query().Get("job") != "") {
		d.Job = f.Job
	}
	if d.Job != nil {
		d.Running = d.Job.State == "running"
		d.Steps, d.Percent = jobProgress(*d.Job, agent.Site{})
		for _, st := range d.Job.Steps {
			if strings.HasPrefix(st, "done ") {
				d.Result = strings.TrimPrefix(st, "done ")
			}
		}
		d.Choose = chooseList(d.Job.Error)
	}
	s.render(w, r, "migrate", page{Title: s.tr.T(s.lang(r), "mig.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// chooseList reads the WordPress folders out of a job that stopped to ask which one ("choose: …; choose one: a, b").
func chooseList(e string) []string {
	if !strings.HasPrefix(e, "choose: ") {
		return nil
	}
	_, list, ok := strings.Cut(e, "choose one: ")
	if !ok {
		return nil
	}
	var out []string
	for _, x := range strings.Split(list, ", ") {
		if x = strings.TrimSpace(x); x != "" && len(x) <= 1024 {
			out = append(out, x)
		}
	}
	return out
}

// codeHost is the old site's host name in a migration code, for the audit log ("" when unreadable).
func codeHost(code string) string {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(strings.TrimSpace(code), "nm1."), "="))
	if err != nil {
		return ""
	}
	var v struct {
		U string `json:"u"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	u, err := url.Parse(v.U)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// migrateAction starts a migration by plugin code or download link, re-runs a backup migration with the chosen
// folder, or removes the uploaded files.
func (s *Server) migrateAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	if !site.IsWP() {
		s.notFound(w, r)
		return
	}
	f, u, l := r.PostForm, userOf(r), s.lang(r)
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	base := "/migrate/" + site.ID
	back := func(msg string) { http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	op := f.Get("op")
	if op == "clear" {
		if err := s.ag.MigrateClear(ctx, site.ID); err != nil {
			s.audit1(u, r, "migrate.clear", site.ID+" failed: "+err.Error())
			back(err.Error())
			return
		}
		s.audit1(u, r, "migrate.clear", site.ID)
		http.Redirect(w, r, base+"?done=clear", http.StatusSeeOther)
		return
	}
	if f.Get("confirm") != "1" {
		back(s.tr.T(l, "mig.confirm.need"))
		return
	}
	var req agent.MigrateRequest
	detail := site.ID
	switch op {
	case "plugin":
		req = agent.MigrateRequest{Way: "plugin", Code: strings.TrimSpace(f.Get("code"))}
		if req.Code == "" || len(req.Code) > 2048 {
			back(s.tr.T(l, "mig.code.need"))
			return
		}
		detail += " plugin from " + codeHost(req.Code)
	case "link":
		req = agent.MigrateRequest{Way: "backup", Link: strings.TrimSpace(f.Get("link"))}
		lu, err := url.Parse(req.Link)
		if err != nil || lu.Scheme != "https" || lu.Host == "" || len(req.Link) > 4096 {
			back(s.tr.T(l, "mig.link.bad"))
			return
		}
		detail += " backup link " + lu.Hostname()
	case "backup", "choose":
		req = agent.MigrateRequest{Way: "backup", Link: strings.TrimSpace(f.Get("link")), Root: f.Get("root")}
		if op == "choose" && req.Root == "" {
			back(s.tr.T(l, "mig.choose.need"))
			return
		}
		detail += " backup"
		if req.Root != "" {
			detail += " folder " + req.Root
		}
	default:
		http.Error(w, "bad request", 400)
		return
	}
	job, err := s.ag.StartMigrate(ctx, site.ID, req)
	if err != nil {
		s.audit1(u, r, "migrate.start", detail+" failed: "+err.Error())
		back(err.Error())
		return
	}
	s.audit1(u, r, "migrate.start", detail)
	http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
}

// migrateUpload streams the backup (and the database file, if given) to the agent, then starts the migration.
func (s *Server) migrateUpload(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	if !site.IsWP() {
		s.notFound(w, r)
		return
	}
	long(w, 12*time.Hour)
	r.Body = http.MaxBytesReader(w, r.Body, maxMigrateBody)
	mr, ok := s.multipartCSRF(r)
	if !ok {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	l, u := s.lang(r), userOf(r)
	ctx, cancel := s.agentCtx(r, 12*time.Hour)
	defer cancel()
	confirmed := false
	var names []string
	var ferr error
	for ferr == nil {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			ferr = err
			break
		}
		switch name := p.FormName(); name {
		case "confirm":
			v, _ := io.ReadAll(io.LimitReader(p, 8))
			confirmed = string(v) == "1"
		case "archive", "database":
			fname := path.Base(strings.ReplaceAll(p.FileName(), `\`, "/"))
			if p.FileName() == "" {
				break // the optional database file was left empty
			}
			low := strings.ToLower(fname)
			switch {
			case !confirmed:
				ferr = errors.New(s.tr.T(l, "mig.confirm.need"))
			case name == "archive" && !(strings.HasSuffix(low, ".tar.gz") || strings.HasSuffix(low, ".tgz") || strings.HasSuffix(low, ".tar") || strings.HasSuffix(low, ".zip")):
				ferr = errors.New(s.tr.T(l, "mig.type.archive"))
			case name == "database" && !(strings.HasSuffix(low, ".sql") || strings.HasSuffix(low, ".sql.gz")):
				ferr = errors.New(s.tr.T(l, "mig.type.database"))
			default:
				ferr = s.ag.MigrateUpload(ctx, site.ID, name, p)
				names = append(names, name+" "+fname)
			}
		}
		p.Close()
	}
	if ferr == nil && (len(names) == 0 || !strings.HasPrefix(names[0], "archive ")) {
		ferr = errors.New(s.tr.T(l, "mig.archive.need"))
	}
	base := "/migrate/" + site.ID
	if ferr != nil {
		var mbe *http.MaxBytesError
		msg := ferr.Error()
		if errors.As(ferr, &mbe) {
			msg = s.tr.T(l, "files.err.toobig")
		}
		s.audit1(u, r, "migrate.upload", site.ID+" "+strings.Join(names, ", ")+" failed: "+msg)
		http.Redirect(w, r, base+"?err="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "migrate.upload", site.ID+" "+strings.Join(names, ", "))
	job, err := s.ag.StartMigrate(ctx, site.ID, agent.MigrateRequest{Way: "backup"})
	if err != nil {
		s.audit1(u, r, "migrate.start", site.ID+" backup failed: "+err.Error())
		http.Redirect(w, r, base+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "migrate.start", site.ID+" backup")
	http.Redirect(w, r, base+"?job="+job+"#job", http.StatusSeeOther)
}

// pluginZip is the Nostek Migrate plugin as WordPress installs it (Plugins → Add New → Upload).
func pluginZip(assets fs.FS) ([]byte, error) {
	src, err := fs.ReadFile(assets, migratePlugin)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "nostek-migrate/nostek-migrate.php", Method: zip.Deflate, Modified: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	h.SetMode(0o644)
	fw, err := zw.CreateHeader(h)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(src); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (s *Server) migratePluginZip(w http.ResponseWriter, r *http.Request) {
	b := s.plugin
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="nostek-migrate.zip"`)
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
