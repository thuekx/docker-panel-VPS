// Package api is the agent's HTTP interface on a Unix socket. It offers only the fixed actions below; every
// other path answers 404 and every body is checked strictly (size, JSON shape, no unknown fields).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/thuekx/docker-panel-VPS/agent/internal/audit"
	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// MaxBody is the largest request body accepted.
const MaxBody = 16 << 10

type ctxKey struct{}

// PeerUID returns the caller's uid stored by ConnContext (-1 when unknown).
func PeerUID(ctx context.Context) int {
	if v, ok := ctx.Value(ctxKey{}).(int); ok {
		return v
	}
	return -1
}

// ConnContext stores the peer uid of a Unix socket connection (SO_PEERCRED).
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	uid := -1
	if uc, ok := c.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				if cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); err == nil {
					uid = int(cred.Uid)
				}
			})
		}
	}
	return context.WithValue(ctx, ctxKey{}, uid)
}

// Server holds the handlers.
type Server struct {
	M       *sites.Manager
	Audit   *audit.Log
	Allowed map[int]bool // peer uids allowed to call; nil = any (tests)
	Version string
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/catalog", s.catalog)
	mux.HandleFunc("GET /v1/sites", s.list)
	mux.HandleFunc("POST /v1/sites/plan", s.plan)
	mux.HandleFunc("POST /v1/sites", s.create)
	mux.HandleFunc("GET /v1/sites/{id}", s.get)
	mux.HandleFunc("POST /v1/sites/{id}/start", s.action("start"))
	mux.HandleFunc("POST /v1/sites/{id}/stop", s.action("stop"))
	mux.HandleFunc("POST /v1/sites/{id}/restart", s.action("restart"))
	mux.HandleFunc("POST /v1/sites/{id}/delete", s.delete)
	mux.HandleFunc("GET /v1/sites/{id}/logs", s.logs)
	mux.HandleFunc("GET /v1/sites/{id}/stack", s.stack)
	mux.HandleFunc("POST /v1/sites/{id}/services/{svc}/restart", s.restartService)
	mux.HandleFunc("GET /v1/sites/{id}/files", s.filesList)
	mux.HandleFunc("GET /v1/sites/{id}/files/content", s.fileGet)
	mux.HandleFunc("PUT /v1/sites/{id}/files/content", s.filePut)
	mux.HandleFunc("GET /v1/sites/{id}/files/zip", s.fileZip)
	mux.HandleFunc("POST /v1/sites/{id}/files/mkdir", s.fileOp("mkdir"))
	mux.HandleFunc("POST /v1/sites/{id}/files/rename", s.fileOp("rename"))
	mux.HandleFunc("POST /v1/sites/{id}/files/chmod", s.fileOp("chmod"))
	mux.HandleFunc("POST /v1/sites/{id}/files/delete", s.fileOp("delete"))
	mux.HandleFunc("POST /v1/sites/{id}/files/unzip", s.fileOp("unzip"))
	mux.HandleFunc("GET /v1/sites/{id}/files/trash", s.filesTrash)
	mux.HandleFunc("POST /v1/sites/{id}/files/trash/{entry}/restore", s.fileRestore)
	mux.HandleFunc("GET /v1/sites/{id}/db", s.db)
	mux.HandleFunc("POST /v1/sites/{id}/db/query", s.dbQuery)
	mux.HandleFunc("GET /v1/sites/{id}/db/export", s.dbExport)
	mux.HandleFunc("GET /v1/sites/{id}/db/backups/{name}", s.dbBackup)
	mux.HandleFunc("PUT /v1/sites/{id}/db/import", s.dbImport)
	mux.HandleFunc("GET /v1/sites/{id}/wp", s.wpGet)
	mux.HandleFunc("POST /v1/sites/{id}/wp/login", s.wpLogin)
	mux.HandleFunc("POST /v1/sites/{id}/wp/plugins", s.wpItems("plugin"))
	mux.HandleFunc("POST /v1/sites/{id}/wp/themes", s.wpItems("theme"))
	mux.HandleFunc("POST /v1/sites/{id}/wp/core", s.wpCore)
	mux.HandleFunc("POST /v1/sites/{id}/wp/settings", s.wpSet)
	mux.HandleFunc("POST /v1/sites/{id}/wp/check", s.wpCheck)
	mux.HandleFunc("POST /v1/sites/{id}/wp/cli", s.wpCLI)
	mux.HandleFunc("GET /v1/sites/{id}/backups", s.backups)
	mux.HandleFunc("POST /v1/sites/{id}/backups", s.backupStart)
	mux.HandleFunc("POST /v1/sites/{id}/backups/schedule", s.backupSchedule)
	mux.HandleFunc("GET /v1/sites/{id}/backups/{name}", s.backupGet)
	mux.HandleFunc("POST /v1/sites/{id}/backups/{name}/delete", s.backupDelete)
	mux.HandleFunc("POST /v1/sites/{id}/backups/{name}/restore", s.backupRestore)
	mux.HandleFunc("GET /v1/trash", s.trash)
	mux.HandleFunc("POST /v1/trash/{entry}/restore", s.restore)
	mux.HandleFunc("GET /v1/jobs/{id}", s.job)
	mux.HandleFunc("POST /v1/jobs/{id}/secret", s.secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		uid := PeerUID(r.Context())
		if s.Allowed != nil && !s.Allowed[uid] {
			s.Audit.Write(audit.Entry{Peer: uid, Action: r.Method + " " + r.URL.Path, Result: "refused", Detail: "peer not allowed"})
			writeErr(w, http.StatusForbidden, "caller not allowed")
			return
		}
		if r.URL.RawQuery != "" && !strings.HasSuffix(r.URL.Path, "/logs") && !queryRoute(r.URL.Path) {
			writeErr(w, http.StatusBadRequest, "no query parameters on this route")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) note(r *http.Request, action, site, result, detail string) {
	s.Audit.Write(audit.Entry{Peer: PeerUID(r.Context()), Action: action, Site: site, Result: result, Detail: detail})
}

func decode(r *http.Request, v any) error { return decodeN(r, v, MaxBody) }

func decodeN(r *http.Request, v any, max int64) error {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		return errors.New("content type must be application/json")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, max+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("body is not valid: " + err.Error())
	}
	if dec.More() {
		return errors.New("body has extra data")
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "version": s.Version})
}

type phpView struct {
	Version       string `json:"version"`
	EOL           bool   `json:"eol"`
	SecurityUntil string `json:"security_until"`
	Default       bool   `json:"default"`
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	var out []phpView
	for _, p := range catalog.All() {
		out = append(out, phpView{p.Version, p.EOL, p.SecurityUntil, p.Version == catalog.Default})
	}
	writeJSON(w, 200, map[string]any{"php": out, "servers": []string{"apache", "nginx"}})
}

type siteView struct {
	sites.Site
	Status string `json:"status"`
	Up     int    `json:"containers_up"`
	Total  int    `json:"containers_total"`
}

func (s *Server) view(r *http.Request, x sites.Site) siteView {
	st, up, total := s.M.StatusCount(r.Context(), x.ID)
	return siteView{x, st, up, total}
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	all, err := s.M.List()
	if err != nil {
		writeErr(w, 500, "cannot read sites")
		return
	}
	out := []siteView{}
	for _, x := range all {
		out = append(out, s.view(r, x))
	}
	writeJSON(w, 200, map[string]any{"sites": out})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	x, err := s.M.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "site not found")
		return
	}
	writeJSON(w, 200, s.view(r, x))
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var c spec.Create
	if err := decode(r, &c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	x, err := s.M.Plan(r.Context(), c)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	x.AdminUser = "" // chosen again at create time
	writeJSON(w, 200, x)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var c spec.Create
	if err := decode(r, &c); err != nil {
		s.note(r, "create", "", "refused", err.Error())
		writeErr(w, 400, err.Error())
		return
	}
	job, x, err := s.M.Create(context.Background(), c)
	if errors.Is(err, sites.ErrBusy) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		s.note(r, "create", c.Domain, "refused", err.Error())
		writeErr(w, 422, err.Error())
		return
	}
	s.note(r, "create", x.ID, "ok", "job "+job+" server="+x.Server+" php="+x.PHP)
	writeJSON(w, 202, map[string]any{"job": job, "site": x})
}

func (s *Server) action(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := s.M.Action(r.Context(), id, name)
		s.result(w, r, name, id, err, map[string]any{"ok": true})
	}
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, action, id string, err error, ok any) {
	switch {
	case errors.Is(err, sites.ErrNotFound):
		s.note(r, action, id, "refused", "not found")
		writeErr(w, 404, "site not found")
	case errors.Is(err, sites.ErrBusy):
		writeErr(w, 409, err.Error())
	case err != nil:
		s.note(r, action, id, "failed", err.Error())
		writeErr(w, 500, err.Error())
	default:
		s.note(r, action, id, "ok", "")
		writeJSON(w, 200, ok)
	}
}

func (s *Server) stack(w http.ResponseWriter, r *http.Request) {
	st, err := s.M.Stack(r.Context(), r.PathValue("id"))
	if errors.Is(err, sites.ErrNotFound) {
		writeErr(w, 404, "site not found")
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	id, svc := r.PathValue("id"), r.PathValue("svc")
	err := s.M.RestartService(r.Context(), id, svc)
	if errors.Is(err, sites.ErrBadInput) {
		s.note(r, "restart-service", id, "refused", err.Error())
		writeErr(w, 400, err.Error())
		return
	}
	s.result(w, r, "restart-service "+svc, id, err, map[string]any{"ok": true})
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, err := s.M.Delete(r.Context(), id)
	s.result(w, r, "delete", id, err, map[string]any{"trash": entry, "days": sites.TrashDays})
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	entry := r.PathValue("entry")
	id, err := s.M.Restore(r.Context(), entry)
	s.result(w, r, "restore", entry, err, map[string]any{"site": id})
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request) {
	t, err := s.M.Trash()
	if err != nil {
		writeErr(w, 500, "cannot read trash")
		return
	}
	if t == nil {
		t = []sites.Trashed{}
	}
	writeJSON(w, 200, map[string]any{"trash": t})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "service" && k != "lines" {
			writeErr(w, 400, "unknown parameter "+k)
			return
		}
	}
	lines, err := strconv.Atoi(q.Get("lines"))
	if err != nil {
		lines = 200
	}
	out, err := s.M.Logs(r.Context(), r.PathValue("id"), q.Get("service"), lines)
	if errors.Is(err, sites.ErrNotFound) {
		writeErr(w, 404, "site not found")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"logs": out})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	j := s.M.Jobs().Get(r.PathValue("id"))
	if j == nil {
		writeErr(w, 404, "job not found")
		return
	}
	writeJSON(w, 200, j.View())
}

func (s *Server) secret(w http.ResponseWriter, r *http.Request) {
	j := s.M.Jobs().Get(r.PathValue("id"))
	if j == nil {
		writeErr(w, 404, "job not found")
		return
	}
	v := j.View()
	sec := j.TakeSecret()
	if sec == "" {
		s.note(r, "secret", v.Site, "refused", "already taken or none")
		writeErr(w, 410, "secret already shown or not available")
		return
	}
	s.note(r, "secret", v.Site, "ok", "shown once")
	writeJSON(w, 200, map[string]string{"password": sec})
}
