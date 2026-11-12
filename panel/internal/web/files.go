package web

// File manager and database pages (SITE-02). The panel never touches site files or the database itself: every
// call goes to nostek-agent, which works only inside the website's own folder and its own database container.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

const (
	maxEdit       = 2 << 20   // largest file opened in the editor (same as the agent)
	maxUploadReq  = 1 << 30   // one upload request, all files together
	maxImportBody = 513 << 20 // database import file plus form overhead
)

// long lifts the server's 30-second deadlines for one transfer.
func long(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(d))
	_ = rc.SetWriteDeadline(time.Now().Add(d))
}

// cleanDir normalises a path from the query ("" = "/").
func cleanDir(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) || len(p) > 1024 {
		return "/"
	}
	return path.Clean(p)
}

type crumb struct{ Name, Path string }

func crumbs(p string) []crumb {
	out := []crumb{{"/", "/"}}
	acc := ""
	for _, part := range strings.Split(strings.Trim(p, "/"), "/") {
		if part == "" {
			continue
		}
		acc += "/" + part
		out = append(out, crumb{part, acc})
	}
	return out
}

type fileRow struct {
	agent.Entry
	Path     string
	Human    string
	When     string
	Editable bool
	Zip      bool
	Kind     string // css class: dir, php, img, zip, code, file, link
}

type fileTrashRow struct {
	agent.FileTrash
	DeletedAt, PurgeAt string
}

type filesData struct {
	Site      agent.Site
	Path      string
	Parent    string
	Crumbs    []crumb
	Rows      []fileRow
	Truncated bool
	Trash     []fileTrashRow
	Err       string
	MaxUpload string
}

func kindOf(e agent.Entry) string {
	switch e.Type {
	case "dir":
		return "dir"
	case "link":
		return "link"
	}
	switch strings.ToLower(path.Ext(e.Name)) {
	case ".php":
		return "php"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg", ".ico":
		return "img"
	case ".zip", ".gz", ".tgz", ".tar":
		return "zip"
	case ".css", ".js", ".json", ".html", ".htm", ".txt", ".md", ".xml", ".ini", ".htaccess", ".conf", ".yml", ".yaml", ".sql":
		return "code"
	}
	return "file"
}

// siteOf reads the site for a files or database page; false = already answered (404).
func (s *Server) siteOf(w http.ResponseWriter, r *http.Request) (agent.Site, bool) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) {
		s.notFound(w, r)
		return agent.Site{}, false
	}
	ctx, cancel := s.agentCtx(r, 10*time.Second)
	defer cancel()
	site, err := s.ag.Site(ctx, id)
	if err != nil {
		s.notFound(w, r)
		return agent.Site{}, false
	}
	return site, true
}

func (s *Server) filesPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	dir := cleanDir(r.URL.Query().Get("path"))
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	d := filesData{Site: site, Path: dir, Crumbs: crumbs(dir), MaxUpload: "256 MB"}
	if dir != "/" {
		d.Parent = path.Dir(dir)
	}
	l, err := s.ag.Files(ctx, site.ID, dir)
	if err != nil {
		d.Err = err.Error()
	}
	d.Truncated = l.Truncated
	for _, e := range l.Entries {
		row := fileRow{Entry: e, Path: path.Join(dir, e.Name), Kind: kindOf(e),
			When: time.Unix(e.Modified, 0).In(s.loc()).Format("02 Jan 2006 15:04")}
		if e.Type == "file" {
			row.Human = human(e.Size)
			row.Editable = e.Size <= maxEdit
			row.Zip = strings.HasSuffix(strings.ToLower(e.Name), ".zip")
		}
		d.Rows = append(d.Rows, row)
	}
	if tr, err := s.ag.FilesTrash(ctx, site.ID); err == nil {
		for _, t := range tr {
			d.Trash = append(d.Trash, fileTrashRow{t, time.Unix(t.Deleted, 0).In(s.loc()).Format("02 Jan 2006 15:04"),
				time.Unix(t.Purge, 0).In(s.loc()).Format("02 Jan 2006 15:04")})
		}
	}
	code := http.StatusOK
	if d.Err != "" && agent.Status(err) == 0 {
		code = http.StatusBadGateway
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	s.render(w, r, "files", page{Title: s.tr.T(s.lang(r), "files.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func filesURL(id, dir string, kv ...string) string {
	q := url.Values{"path": {dir}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	return "/files/" + id + "?" + q.Encode()
}

// filesAction handles the small forms of the files page: new folder or file, rename, permissions, delete, unzip,
// restore from the trash and saving the editor.
func (s *Server) filesAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	f := r.PostForm
	op, dir := f.Get("op"), cleanDir(f.Get("dir"))
	target := cleanDir(f.Get("path"))
	ctx, cancel := s.agentCtx(r, 30*time.Minute)
	defer cancel()
	long(w, 30*time.Minute)
	var err error
	detail, done, back := target, op, ""
	switch op {
	case "mkdir", "newfile":
		name := strings.TrimSpace(f.Get("name"))
		if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
			err = errors.New(s.tr.T(s.lang(r), "files.err.name"))
			break
		}
		target = path.Join(dir, name)
		detail = target
		if op == "mkdir" {
			_, err = s.ag.FileOp(ctx, site.ID, "mkdir", target, "", "")
		} else {
			_, err = s.ag.PutFile(ctx, site.ID, target, false, strings.NewReader(""))
			back = "/files/" + site.ID + "/edit?" + url.Values{"path": {target}}.Encode()
		}
	case "rename":
		name := strings.TrimSpace(f.Get("name"))
		if name == "" || strings.ContainsRune(name, 0) {
			err = errors.New(s.tr.T(s.lang(r), "files.err.name"))
			break
		}
		to := name
		if !strings.HasPrefix(name, "/") { // a plain name stays in the same folder; "/x/y" moves
			to = path.Join(path.Dir(target), name)
		}
		detail = target + " -> " + to
		_, err = s.ag.FileOp(ctx, site.ID, "rename", target, to, "")
	case "chmod":
		mode := strings.TrimSpace(f.Get("mode"))
		detail = target + " " + mode
		_, err = s.ag.FileOp(ctx, site.ID, "chmod", target, "", mode)
	case "delete":
		_, err = s.ag.FileOp(ctx, site.ID, "delete", target, "", "")
	case "unzip":
		var out map[string]any
		out, err = s.ag.FileOp(ctx, site.ID, "unzip", target, "", "")
		if n, ok := out["files"].(float64); ok {
			detail += fmt.Sprintf(" (%d files)", int(n))
		}
	case "restore":
		entry := f.Get("entry")
		var p string
		p, err = s.ag.RestoreFile(ctx, site.ID, entry)
		detail = entry + " " + p
		if err == nil {
			dir = path.Dir(p)
		}
	case "save":
		body := f.Get("content")
		if f.Get("eol") != "crlf" { // browsers send CRLF from a textarea; keep the file's own line ends
			body = strings.ReplaceAll(body, "\r\n", "\n")
		}
		detail = fmt.Sprintf("%s (%d bytes)", target, len(body))
		_, err = s.ag.PutFile(ctx, site.ID, target, true, strings.NewReader(body))
		back = "/files/" + site.ID + "/edit?" + url.Values{"path": {target}, "done": {"save"}}.Encode()
		dir = path.Dir(target)
	default:
		http.Error(w, "bad request", 400)
		return
	}
	if err != nil {
		s.audit1(userOf(r), r, "files."+op, site.ID+" "+detail+" failed: "+err.Error())
		if op == "save" {
			http.Redirect(w, r, "/files/"+site.ID+"/edit?"+url.Values{"path": {target}, "err": {err.Error()}}.Encode(), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, filesURL(site.ID, dir, "err", err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "files."+op, site.ID+" "+detail)
	if back == "" {
		back = filesURL(site.ID, dir, "done", done)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// multipartCSRF reads the first part of a multipart form, which must be the csrf field.
func (s *Server) multipartCSRF(r *http.Request) (*multipart.Reader, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, false
	}
	p, err := mr.NextPart()
	if err != nil || p.FormName() != "csrf" {
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(p, 200))
	sess := sessOf(r)
	if sess == nil || !subtleEq(string(b), sess.CSRF) {
		return nil, false
	}
	return mr, true
}

// filesUpload streams the files of a multipart form one by one to the agent; nothing is buffered on disk.
func (s *Server) filesUpload(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	long(w, 2*time.Hour)
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadReq)
	mr, ok := s.multipartCSRF(r)
	if !ok {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	dir := cleanDir(r.URL.Query().Get("path"))
	ctx, cancel := s.agentCtx(r, 2*time.Hour)
	defer cancel()
	overwrite, n := false, 0
	var names []string
	var ferr error
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			ferr = err
			break
		}
		switch p.FormName() {
		case "overwrite":
			v, _ := io.ReadAll(io.LimitReader(p, 8))
			overwrite = string(v) == "1"
		case "files":
			name := path.Base(strings.ReplaceAll(p.FileName(), `\`, "/"))
			if name == "" || name == "." || name == "/" {
				continue
			}
			if _, err := s.ag.PutFile(ctx, site.ID, path.Join(dir, name), overwrite, p); err != nil {
				ferr = fmt.Errorf("%s: %w", name, err)
			} else {
				n++
				names = append(names, name)
			}
		}
		p.Close()
		if ferr != nil {
			break
		}
	}
	detail := fmt.Sprintf("%s %s: %d file(s) %s", site.ID, dir, n, strings.Join(names, ", "))
	if len(detail) > 400 {
		detail = detail[:400] + "…"
	}
	if ferr != nil {
		var mbe *http.MaxBytesError
		msg := ferr.Error()
		if errors.As(ferr, &mbe) {
			msg = s.tr.T(s.lang(r), "files.err.toobig")
		}
		s.audit1(userOf(r), r, "files.upload", detail+" failed: "+msg)
		http.Redirect(w, r, filesURL(site.ID, dir, "err", msg), http.StatusSeeOther)
		return
	}
	if n == 0 {
		http.Redirect(w, r, filesURL(site.ID, dir, "err", s.tr.T(s.lang(r), "files.err.nofile")), http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "files.upload", detail)
	http.Redirect(w, r, filesURL(site.ID, dir, "done", "upload"), http.StatusSeeOther)
}

// relay copies an agent download to the browser as an attachment.
func relay(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	if v := resp.Header.Get("Content-Type"); v == "application/zip" || v == "application/gzip" {
		h.Set("Content-Type", v)
	}
	if v := resp.Header.Get("Content-Disposition"); strings.HasPrefix(v, "attachment; ") {
		h.Set("Content-Disposition", v)
	} else {
		h.Set("Content-Disposition", "attachment")
	}
	if v := resp.Header.Get("Content-Length"); v != "" {
		if _, err := strconv.ParseInt(v, 10, 64); err == nil {
			h.Set("Content-Length", v)
		}
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) filesDownload(zip bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		site, ok := s.siteOf(w, r)
		if !ok {
			return
		}
		p := cleanDir(r.URL.Query().Get("path"))
		long(w, 2*time.Hour)
		ctx, cancel := s.agentCtx(r, 2*time.Hour)
		defer cancel()
		var resp *http.Response
		var err error
		if zip {
			resp, err = s.ag.Zip(ctx, site.ID, p)
		} else {
			resp, err = s.ag.Download(ctx, site.ID, p)
		}
		if err != nil {
			http.Redirect(w, r, filesURL(site.ID, path.Dir(p), "err", err.Error()), http.StatusSeeOther)
			return
		}
		if zip {
			s.audit1(userOf(r), r, "files.zip", site.ID+" "+p)
		}
		relay(w, resp)
	}
}

type editData struct {
	Site    agent.Site
	Path    string
	Dir     string
	Crumbs  []crumb
	Content string
	EOL     string
	Binary  bool
	TooBig  bool
	Size    string
	Lang    string // highlight hint: php, css, js, html, text
}

func (s *Server) filesEdit(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	p := cleanDir(r.URL.Query().Get("path"))
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d := editData{Site: site, Path: p, Dir: path.Dir(p), Crumbs: crumbs(p), EOL: "lf", Lang: strings.TrimPrefix(strings.ToLower(path.Ext(p)), ".")}
	resp, err := s.ag.Download(ctx, site.ID, p)
	if err != nil {
		http.Redirect(w, r, filesURL(site.ID, path.Dir(p), "err", err.Error()), http.StatusSeeOther)
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxEdit+1))
	if err != nil {
		http.Redirect(w, r, filesURL(site.ID, path.Dir(p), "err", err.Error()), http.StatusSeeOther)
		return
	}
	d.Size = human(int64(len(b)))
	switch {
	case len(b) > maxEdit:
		d.TooBig = true
	case bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b):
		d.Binary = true
	default:
		d.Content = string(b)
		if bytes.Contains(b, []byte("\r\n")) {
			d.EOL = "crlf"
		}
	}
	s.render(w, r, "fileedit", page{Title: path.Base(p) + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// ---------- database

type dbData struct {
	Site     agent.Site
	DB       agent.DB
	Err      string
	SQL      string
	Answer   *agent.QueryAnswer
	QueryErr string
	Size     string
	Backups  []dumpRow
	Tables   []tableRow
}

type tableRow struct {
	agent.Table
	Data, Index string
}

type dumpRow struct {
	agent.Dump
	Human, When string
}

func (s *Server) dbPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	s.renderDB(w, r, site, dbData{SQL: r.URL.Query().Get("sql")}, http.StatusOK)
}

func (s *Server) renderDB(w http.ResponseWriter, r *http.Request, site agent.Site, d dbData, code int) {
	ctx, cancel := s.agentCtx(r, 30*time.Second)
	defer cancel()
	d.Site = site
	db, err := s.ag.Database(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.DB = db
	d.Size = human(db.Size)
	for _, t := range db.Tables {
		d.Tables = append(d.Tables, tableRow{t, human(t.Data), human(t.Index)})
	}
	for _, b := range db.Backups {
		d.Backups = append(d.Backups, dumpRow{b, human(b.Size), time.Unix(b.Created, 0).In(s.loc()).Format("02 Jan 2006 15:04")})
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	s.render(w, r, "db", page{Title: s.tr.T(s.lang(r), "db.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

// dbAction runs the SQL console or starts an export download.
func (s *Server) dbAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	switch r.PostForm.Get("op") {
	case "query":
		sql := r.PostForm.Get("sql")
		ctx, cancel := s.agentCtx(r, 90*time.Second)
		defer cancel()
		long(w, 2*time.Minute)
		a, err := s.ag.Query(ctx, site.ID, sql)
		kind := strings.Fields(strings.ToUpper(sql + " x"))[0]
		if len(kind) > 12 {
			kind = kind[:12]
		}
		d := dbData{SQL: sql}
		code := http.StatusOK
		if err != nil {
			d.QueryErr = err.Error()
			code = http.StatusUnprocessableEntity
			s.audit1(userOf(r), r, "db.query", fmt.Sprintf("%s %s (%d chars) failed", site.ID, kind, len(sql)))
		} else {
			d.Answer = &a
			s.audit1(userOf(r), r, "db.query", fmt.Sprintf("%s %s (%d chars)", site.ID, kind, len(sql)))
		}
		s.renderDB(w, r, site, d, code)
	case "export":
		long(w, 2*time.Hour)
		ctx, cancel := s.agentCtx(r, 2*time.Hour)
		defer cancel()
		resp, err := s.ag.Export(ctx, site.ID)
		if err != nil {
			s.audit1(userOf(r), r, "db.export", site.ID+" failed: "+err.Error())
			http.Redirect(w, r, "/db/"+site.ID+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		s.audit1(userOf(r), r, "db.export", site.ID)
		relay(w, resp)
	default:
		http.Error(w, "bad request", 400)
	}
}

func (s *Server) dbBackup(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !strings.HasPrefix(name, "before-import-") || !strings.HasSuffix(name, ".sql.gz") || strings.ContainsAny(name, "/\\") {
		s.notFound(w, r)
		return
	}
	long(w, 2*time.Hour)
	ctx, cancel := s.agentCtx(r, 2*time.Hour)
	defer cancel()
	resp, err := s.ag.Backup(ctx, site.ID, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.audit1(userOf(r), r, "db.backup_download", site.ID+" "+name)
	relay(w, resp)
}

// dbImport streams an uploaded .sql or .sql.gz to the agent, which keeps a copy of the database first.
func (s *Server) dbImport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	long(w, 2*time.Hour)
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
	mr, ok := s.multipartCSRF(r)
	if !ok {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	ctx, cancel := s.agentCtx(r, 2*time.Hour)
	defer cancel()
	confirmed, done := false, false
	var backup, fname string
	var ferr error
	for !done {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			ferr = err
			break
		}
		switch p.FormName() {
		case "confirm":
			v, _ := io.ReadAll(io.LimitReader(p, 8))
			confirmed = string(v) == "1"
		case "file":
			fname = path.Base(strings.ReplaceAll(p.FileName(), `\`, "/"))
			low := strings.ToLower(fname)
			switch {
			case !confirmed:
				ferr = errors.New(s.tr.T(s.lang(r), "db.import.confirm.need"))
			case !strings.HasSuffix(low, ".sql") && !strings.HasSuffix(low, ".sql.gz"):
				ferr = errors.New(s.tr.T(s.lang(r), "db.import.type"))
			default:
				backup, ferr = s.ag.Import(ctx, site.ID, p)
			}
			done = true
		}
		p.Close()
	}
	if ferr == nil && fname == "" {
		ferr = errors.New(s.tr.T(s.lang(r), "files.err.nofile"))
	}
	if ferr != nil {
		var mbe *http.MaxBytesError
		msg := ferr.Error()
		if errors.As(ferr, &mbe) {
			msg = s.tr.T(s.lang(r), "files.err.toobig")
		}
		s.audit1(userOf(r), r, "db.import", site.ID+" "+fname+" failed: "+msg)
		http.Redirect(w, r, "/db/"+site.ID+"?err="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	s.audit1(userOf(r), r, "db.import", site.ID+" "+fname+"; copy before import: "+backup)
	http.Redirect(w, r, "/db/"+site.ID+"?done=import", http.StatusSeeOther)
}
