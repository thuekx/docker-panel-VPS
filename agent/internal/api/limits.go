package api

// Resource limits per website (RES-01). The audit log names the changed services and settings with their values
// (sizes, never a secret).

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) limits(w http.ResponseWriter, r *http.Request) {
	l, err := s.M.Limits(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, l)
}

func (s *Server) limitsSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c sites.LimitChange
	if err := decodeN(r, &c, 16<<10); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	long(w, 6*time.Minute)
	if err := s.M.SetLimits(r.Context(), id, c); err != nil {
		s.fail(w, r, "limits", id, err)
		return
	}
	var what []string
	for k, v := range c.RAM {
		what = append(what, fmt.Sprintf("%s %d MB", k, v))
	}
	sort.Strings(what)
	if p := c.PHP; p != nil {
		what = append(what, fmt.Sprintf("php memory %d MB, upload %d MB, time %d s, input vars %d", p.Memory, p.Upload, p.ExecTime, p.InputVars))
	}
	s.note(r, "limits", id, "ok", strings.Join(what, ", "))
	writeJSON(w, 200, map[string]bool{"ok": true})
}
