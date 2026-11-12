package api

// File manager and database routes (SITE-02).

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

// queryRoute tells which routes take query parameters (each handler checks its own keys).
func queryRoute(p string) bool {
	return strings.HasSuffix(p, "/files") || strings.HasSuffix(p, "/files/content") || strings.HasSuffix(p, "/files/zip")
}

// onlyKeys refuses any query parameter outside keys.
func onlyKeys(w http.ResponseWriter, r *http.Request, keys ...string) bool {
	for k := range r.URL.Query() {
		ok := false
		for _, x := range keys {
			ok = ok || k == x
		}
		if !ok {
			writeErr(w, 400, "unknown parameter "+k)
			return false
		}
	}
	return true
}

// long lifts the server's short read and write deadlines for transfers.
func long(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(d))
	_ = rc.SetWriteDeadline(time.Now().Add(d))
}

// fail answers an error from the file or database code with the right status.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, action, id string, err error) {
	code := 500
	switch {
	case errors.Is(err, sites.ErrNotFound):
		code = 404
	case errors.Is(err, sites.ErrBusy), errors.Is(err, sites.ErrExists):
		code = 409
	case errors.Is(err, sites.ErrTooBig):
		code = 413
	case errors.Is(err, sites.ErrBadInput), errors.Is(err, sites.ErrNotNode):
		code = 400
	case errors.Is(err, sites.ErrNoLog):
		code = 404
	}
	if action != "" {
		s.note(r, action, id, "refused", err.Error())
	}
	msg := err.Error()
	msg = strings.TrimPrefix(msg, sites.ErrBadInput.Error()+": ")
	writeErr(w, code, msg)
}

func (s *Server) filesList(w http.ResponseWriter, r *http.Request) {
	if !onlyKeys(w, r, "path") {
		return
	}
	l, err := s.M.ListFiles(r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, l)
}

func download(w http.ResponseWriter, name, ctype string, size int64, body io.Reader) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.Map(func(c rune) rune {
		if c < 32 || c == '"' || c == '\\' || c > 126 {
			return '_'
		}
		return c
	}, name)))
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(200)
	_, _ = io.Copy(w, body)
}

func (s *Server) fileGet(w http.ResponseWriter, r *http.Request) {
	if !onlyKeys(w, r, "path") {
		return
	}
	f, fi, err := s.M.OpenFile(r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	defer f.Close()
	long(w, time.Hour)
	download(w, fi.Name(), "application/octet-stream", fi.Size(), f)
}

func (s *Server) filePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !onlyKeys(w, r, "path", "overwrite") {
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeErr(w, 400, "content type must be application/octet-stream")
		return
	}
	if r.ContentLength > sites.MaxUpload {
		s.fail(w, r, "file-write", id, sites.ErrTooBig)
		return
	}
	long(w, time.Hour)
	q := r.URL.Query()
	e, err := s.M.WriteFile(id, q.Get("path"), r.Body, q.Get("overwrite") == "1")
	if err != nil {
		s.fail(w, r, "file-write", id, err)
		return
	}
	s.note(r, "file-write", id, "ok", fmt.Sprintf("%s (%d bytes)", q.Get("path"), e.Size))
	writeJSON(w, 200, e)
}

func (s *Server) fileZip(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !onlyKeys(w, r, "path") {
		return
	}
	p := r.URL.Query().Get("path")
	// check first so an unknown path answers an error instead of an empty zip
	e, err := s.M.StatFile(id, p)
	if err == nil && e.Type != "dir" && e.Type != "file" {
		err = fmt.Errorf("%w: only files and folders", sites.ErrBadInput)
	}
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	name := e.Name
	if name == "/" || name == "." {
		name = id
	}
	long(w, 2*time.Hour)
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(s.M.ZipFolder(id, p, pw)) }()
	s.note(r, "file-zip", id, "ok", p)
	download(w, name+".zip", "application/zip", -1, pr)
	pr.Close()
}

type fileReq struct {
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
	Mode string `json:"mode,omitempty"`
}

func (s *Server) fileOp(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var q fileReq
		if err := decode(r, &q); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		var out any = map[string]any{"ok": true}
		var err error
		detail := q.Path
		switch op {
		case "mkdir":
			err = s.M.Mkdir(id, q.Path)
		case "rename":
			err = s.M.Rename(id, q.Path, q.To)
			detail += " -> " + q.To
		case "chmod":
			err = s.M.Chmod(id, q.Path, q.Mode)
			detail += " " + q.Mode
		case "delete":
			var t sites.FileTrash
			t, err = s.M.DeleteFile(id, q.Path)
			out = t
		case "unzip":
			long(w, 30*time.Minute)
			var n int
			n, err = s.M.Unzip(id, q.Path)
			out = map[string]int{"files": n}
			detail += fmt.Sprintf(" (%d files)", n)
		}
		if err != nil {
			s.fail(w, r, "file-"+op, id, err)
			return
		}
		s.note(r, "file-"+op, id, "ok", detail)
		writeJSON(w, 200, out)
	}
}

func (s *Server) filesTrash(w http.ResponseWriter, r *http.Request) {
	t, err := s.M.FilesTrash(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, map[string]any{"trash": t, "days": sites.TrashDays})
}

func (s *Server) fileRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.M.RestoreFile(id, r.PathValue("entry"))
	if err != nil {
		s.fail(w, r, "file-restore", id, err)
		return
	}
	s.note(r, "file-restore", id, "ok", p)
	writeJSON(w, 200, map[string]string{"path": p})
}

func (s *Server) db(w http.ResponseWriter, r *http.Request) {
	d, err := s.M.Database(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, d)
}

// firstWord is what the audit keeps of console SQL: the statement kind, never the text (it may hold data).
func firstWord(sql string) string {
	f := strings.Fields(strings.ToUpper(sql))
	if len(f) == 0 {
		return ""
	}
	w := f[0]
	if len(w) > 12 {
		w = w[:12]
	}
	return strings.Map(func(c rune) rune {
		if c >= 'A' && c <= 'Z' {
			return c
		}
		return -1
	}, w)
}

func (s *Server) dbQuery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var q struct {
		SQL string `json:"sql"`
	}
	if err := decodeN(r, &q, sites.MaxSQL*2); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 2*time.Minute)
	a, err := s.M.Query(r.Context(), id, q.SQL)
	detail := fmt.Sprintf("%s (%d chars)", firstWord(q.SQL), len(q.SQL))
	if err != nil {
		s.fail(w, r, "db-query", id, err)
		return
	}
	s.note(r, "db-query", id, "ok", detail)
	writeJSON(w, 200, a)
}

func (s *Server) dbExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	long(w, time.Hour)
	p, done, err := s.M.Export(r.Context(), id)
	if err != nil {
		s.fail(w, r, "db-export", id, err)
		return
	}
	defer done()
	f, err := os.Open(p)
	if err != nil {
		s.fail(w, r, "db-export", id, err)
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	s.note(r, "db-export", id, "ok", fmt.Sprintf("%d bytes", fi.Size()))
	download(w, id+"-"+strings.TrimPrefix(fi.Name(), "export-"), "application/gzip", fi.Size(), f)
}

func (s *Server) dbBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, fi, err := s.M.OpenDump(id, r.PathValue("name"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	defer f.Close()
	long(w, time.Hour)
	s.note(r, "db-backup-download", id, "ok", fi.Name())
	download(w, id+"-"+fi.Name(), "application/gzip", fi.Size(), f)
}

func (s *Server) dbImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeErr(w, 400, "content type must be application/octet-stream")
		return
	}
	if r.ContentLength > sites.MaxImport {
		s.fail(w, r, "db-import", id, sites.ErrTooBig)
		return
	}
	long(w, time.Hour)
	backup, err := s.M.Import(r.Context(), id, r.Body)
	if err != nil {
		if backup != "" {
			s.note(r, "db-import", id, "failed", "copy kept: "+backup+"; "+err.Error())
			writeJSON(w, 422, map[string]string{"error": strings.TrimPrefix(err.Error(), sites.ErrBadInput.Error()+": "), "backup": backup})
			return
		}
		s.fail(w, r, "db-import", id, err)
		return
	}
	s.note(r, "db-import", id, "ok", "copy before import: "+backup)
	writeJSON(w, 200, map[string]string{"backup": backup})
}
