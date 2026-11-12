package api

// WordPress Tools routes (WP-01). The audit log names the action and the plugins or themes, never a login link
// or the arguments of a console command (they may hold a password).

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) wpGet(w http.ResponseWriter, r *http.Request) {
	x, err := s.M.WordPress(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) wpLogin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	link, user, err := s.M.Login(r.Context(), id)
	if err != nil {
		s.fail(w, r, "wp-login", id, err)
		return
	}
	s.note(r, "wp-login", id, "ok", "as "+user)
	writeJSON(w, 200, map[string]string{"url": link, "user": user})
}

func (s *Server) wpItems(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var a sites.ItemAction
		if err := decode(r, &a); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		long(w, 16*time.Minute)
		what := strings.Join(a.Names, ",")
		if a.All {
			what = "all"
		}
		if len(what) > 300 {
			what = what[:300] + "..."
		}
		action := "wp-" + kind + "-" + a.Do
		out, err := s.M.Items(r.Context(), id, kind, a)
		if err != nil {
			s.fail(w, r, action, id, err)
			return
		}
		s.note(r, action, id, "ok", what)
		writeJSON(w, 200, map[string]string{"output": out})
	}
}

func (s *Server) wpCore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Do string `json:"do"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 16*time.Minute)
	out, err := s.M.Core(r.Context(), id, a.Do)
	if err != nil {
		s.fail(w, r, "wp-core-"+a.Do, id, err)
		return
	}
	s.note(r, "wp-core-"+a.Do, id, "ok", "")
	writeJSON(w, 200, map[string]string{"output": out})
}

func (s *Server) wpSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Key string `json:"key"`
		On  bool   `json:"on"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.M.Set(r.Context(), id, a.Key, a.On); err != nil {
		s.fail(w, r, "wp-set", id, err)
		return
	}
	s.note(r, "wp-set", id, "ok", fmt.Sprintf("%s=%v", a.Key, a.On))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) wpCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	long(w, 4*time.Minute)
	c, err := s.M.Integrity(r.Context(), id)
	if err != nil {
		s.fail(w, r, "wp-check", id, err)
		return
	}
	s.note(r, "wp-check", id, "ok", fmt.Sprintf("core=%v plugins=%v", c.CoreOK, c.PluginsOK))
	writeJSON(w, 200, c)
}

func (s *Server) wpCLI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		Line string `json:"line"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 4*time.Minute)
	first := "?"
	if args, err := sites.SplitArgs(a.Line); err == nil && cmdWord.MatchString(args[0]) {
		first = args[0]
	}
	detail := fmt.Sprintf("%s (%d chars)", first, len(a.Line))
	res, err := s.M.CLI(r.Context(), id, a.Line)
	if err != nil {
		s.note(r, "wp-cli", id, "refused", detail+": "+err.Error())
		s.fail(w, r, "", "", err)
		return
	}
	s.note(r, "wp-cli", id, "ok", fmt.Sprintf("%s exit %d", detail, res.Code))
	writeJSON(w, 200, res)
}

// cmdWord is a WP-CLI command name; anything else (a flag, a value) is not written to the audit log.
var cmdWord = regexp.MustCompile(`^[a-z][a-z-]{0,19}$`)
