package api

// Terminal routes (TERM-01): the terminal card, on/off, and a session over a WebSocket. Binary messages carry the
// terminal's bytes both ways; a text message {"cols":N,"rows":N} from the panel resizes it. What is typed is never
// logged: the audit notes who opened a session, when it ended and how long it ran.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Terminal(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, "", "", err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) terminalSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a struct {
		On bool `json:"on"`
	}
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.M.SetTerminal(id, a.On); err != nil {
		s.fail(w, r, "terminal-set", id, err)
		return
	}
	s.note(r, "terminal-set", id, "ok", strconv.FormatBool(a.On))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// termSize reads cols and rows, bounded to what a screen can show.
func termSize(q string, def uint16) uint16 {
	n, err := strconv.Atoi(q)
	if err != nil || n < 10 || n > 500 {
		return def
	}
	return uint16(n)
}

func (s *Server) terminalSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !onlyKeys(w, r, "user", "cols", "rows") {
		return
	}
	by := r.URL.Query().Get("user")
	if _, err := strconv.Atoi(by); err != nil || len(by) > 12 {
		writeErr(w, 400, "user must be the panel user's id")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sites.TermMax)
	defer cancel()
	sess, err := s.M.OpenTerminal(ctx, id)
	if err != nil {
		s.fail(w, r, "terminal", id, err)
		return
	}
	defer sess.Done()
	rc := http.NewResponseController(w) // the server's timeouts must not end a session
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 10)
	ws := &sizeWin{Cols: termSize(r.URL.Query().Get("cols"), 100), Rows: termSize(r.URL.Query().Get("rows"), 30)}
	f, err := pty.StartWithSize(sess.Cmd, &pty.Winsize{Cols: ws.Cols, Rows: ws.Rows})
	if err != nil {
		s.note(r, "terminal", id, "refused", "user "+by+": "+err.Error())
		_ = c.Close(websocket.StatusInternalError, "the shell did not start")
		return
	}
	start := time.Now()
	s.note(r, "terminal", id, "ok", fmt.Sprintf("open by user %s in %s as %s", by, sess.Target.Service, sess.Target.User))
	why := s.pump(ctx, c, f)
	_ = f.Close()
	if sess.Cmd.Process != nil {
		_ = sess.Cmd.Process.Signal(syscall.SIGHUP)
	}
	done := make(chan struct{})
	go func() { _ = sess.Cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if sess.Cmd.Process != nil {
			_ = sess.Cmd.Process.Kill()
		}
		<-done
	}
	s.note(r, "terminal-end", id, "ok", fmt.Sprintf("user %s, %s, %s", by, time.Since(start).Round(time.Second), why))
	_ = c.Close(websocket.StatusNormalClosure, why)
}

type sizeWin struct{ Cols, Rows uint16 }

// pump copies the terminal both ways until the shell ends, the panel goes, the session is idle or too long.
func (s *Server) pump(parent context.Context, c *websocket.Conn, f *os.File) string {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	last := time.Now()
	why := "closed"
	set := func(w string) {
		mu.Lock()
		if why == "closed" {
			why = w
		}
		mu.Unlock()
		cancel()
	}
	go func() { // shell → panel
		buf := make([]byte, 16<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := c.Write(wctx, websocket.MessageBinary, buf[:n])
				wcancel()
				if err != nil {
					set("panel gone")
					return
				}
			}
			if err != nil {
				set("shell ended")
				return
			}
		}
	}()
	go func() { // panel → shell
		for {
			t, b, err := c.Read(context.Background()) // a cancelled read would drop the connection before the close frame
			if err != nil {
				set("panel closed")
				return
			}
			mu.Lock()
			last = time.Now()
			mu.Unlock()
			if t == websocket.MessageText {
				var sz struct {
					Cols int `json:"cols"`
					Rows int `json:"rows"`
				}
				if json.Unmarshal(b, &sz) == nil && sz.Cols >= 10 && sz.Cols <= 500 && sz.Rows >= 5 && sz.Rows <= 300 {
					_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(sz.Cols), Rows: uint16(sz.Rows)})
				}
				continue
			}
			if _, err := f.Write(b); err != nil {
				set("shell ended")
				return
			}
		}
	}()
	tick := time.NewTicker(min(time.Minute, s.termIdle()/2))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(parent.Err(), context.DeadlineExceeded) {
				return "longer than " + sites.TermMax.String()
			}
			mu.Lock()
			defer mu.Unlock()
			return why
		case <-tick.C:
			mu.Lock()
			idle := time.Since(last) > s.termIdle()
			mu.Unlock()
			if idle {
				set("idle")
			}
		}
	}
}

func (s *Server) termIdle() time.Duration {
	if s.TermIdle > 0 {
		return s.TermIdle
	}
	return sites.TermIdle
}
