package web

// Terminal page (TERM-01): a shell inside the website's own container, in the browser (xterm.js). It is off until
// it is turned on for the website; a session is a WebSocket from the browser to the panel, which checks the
// sign-in, the origin and the CSRF token and joins it to the agent's session. The audit notes who opened a session,
// when it ended and how long it ran, never what was typed.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

type termData struct {
	Site agent.Site
	V    agent.TerminalView
	Err  string
}

func (s *Server) termPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.agentCtx(r, 15*time.Second)
	defer cancel()
	d := termData{Site: site}
	v, err := s.ag.Terminal(ctx, site.ID)
	if err != nil {
		d.Err = err.Error()
	}
	d.V = v
	s.render(w, r, "terminal", page{Title: s.tr.T(s.lang(r), "term.title") + " · " + site.Domain, Active: "sites", Data: d,
		Flash: r.URL.Query().Get("done"), Error: r.URL.Query().Get("err")})
}

func (s *Server) termAction(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	u := userOf(r)
	ctx, cancel := s.agentCtx(r, 15*time.Second)
	defer cancel()
	on := r.PostForm.Get("op") == "on"
	if op := r.PostForm.Get("op"); op != "on" && op != "off" {
		http.Error(w, "bad request", 400)
		return
	}
	detail := site.ID + " " + map[bool]string{true: "on", false: "off"}[on]
	if err := s.ag.SetTerminal(ctx, site.ID, on); err != nil {
		s.audit1(u, r, "site.terminal", detail+" failed: "+err.Error())
		http.Redirect(w, r, "/terminal/"+site.ID+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.audit1(u, r, "site.terminal", detail)
	http.Redirect(w, r, "/terminal/"+site.ID+"?done="+map[bool]string{true: "on", false: "off"}[on], http.StatusSeeOther)
}

// termSocket joins the browser to a terminal session of the agent.
func (s *Server) termSocket(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteOf(w, r)
	if !ok {
		return
	}
	sess, u := sessOf(r), userOf(r)
	if sess == nil {
		http.Error(w, "bad request", http.StatusForbidden)
		return
	}
	rc := http.NewResponseController(w) // the server's timeouts must not end a session
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	// same origin only (the default of Accept): a page elsewhere cannot open a shell with the panel's cookie
	down, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer down.CloseNow()
	// the first message carries the CSRF token (never in the address, which lands in access logs) and the size
	hctx, hcancel := context.WithTimeout(r.Context(), 10*time.Second)
	t, b, err := down.Read(hctx)
	hcancel()
	var hello struct {
		CSRF string `json:"csrf"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err != nil || t != websocket.MessageText || len(b) > 1024 || json.Unmarshal(b, &hello) != nil || !subtleEq(hello.CSRF, sess.CSRF) {
		_ = down.Close(websocket.StatusPolicyViolation, "not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour+time.Minute)
	defer cancel()
	up, err := s.ag.DialTerminal(ctx, site.ID, u.ID, hello.Cols, hello.Rows)
	if err != nil {
		s.audit1(u, r, "site.terminal-open", site.ID+" refused: "+err.Error())
		msg := err.Error()
		if len(msg) > 100 {
			msg = msg[:100]
		}
		_ = down.Close(websocket.StatusTryAgainLater, msg)
		return
	}
	defer up.CloseNow()
	down.SetReadLimit(64 << 10)
	up.SetReadLimit(1 << 20)
	start := time.Now()
	s.audit1(u, r, "site.terminal-open", site.ID)
	var in, out atomic.Int64
	type stop struct{ side, reason string }
	why := make(chan stop, 2)
	pipe := func(from, to *websocket.Conn, n *atomic.Int64, side string) {
		for {
			t, b, err := from.Read(context.Background())
			if err != nil {
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					why <- stop{side, ce.Reason}
				} else {
					why <- stop{side, ""}
				}
				return
			}
			n.Add(int64(len(b)))
			wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = to.Write(wctx, t, b)
			wcancel()
			if err != nil {
				why <- stop{side, ""}
				return
			}
		}
	}
	go pipe(down, up, &in, "browser")
	go pipe(up, down, &out, "server")
	var end stop
	select {
	case end = <-why:
	case <-ctx.Done():
		end = stop{"panel", "timeout"}
	}
	reason := end.reason
	if len(reason) > 100 {
		reason = reason[:100]
	}
	_ = up.Close(websocket.StatusNormalClosure, "")
	_ = down.Close(websocket.StatusNormalClosure, reason)
	s.audit1(u, r, "site.terminal-close", site.ID+" after "+time.Since(start).Round(time.Second).String()+", "+
		strconv.FormatInt(in.Load(), 10)+" bytes in, "+strconv.FormatInt(out.Load(), 10)+" bytes out (by the "+end.side+" "+reason+")")
}
