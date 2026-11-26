package agent

// Terminal calls (TERM-01): the card, on/off, and a session over a WebSocket on the agent's socket.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
)

// TermTarget is where a website's terminal opens.
type TermTarget struct {
	Service string `json:"service"`
	User    string `json:"user"`
	Dir     string `json:"dir"`
}

// TerminalView is the terminal card of a website.
type TerminalView struct {
	Possible bool        `json:"possible"`
	Why      string      `json:"why"`
	On       bool        `json:"on"`
	Target   *TermTarget `json:"target"`
	Open     int         `json:"open"`
	Max      int         `json:"max"`
	IdleMin  int         `json:"idle_min"`
	MaxMin   int         `json:"max_min"`
}

// Terminal returns the terminal card of a website.
func (c *Client) Terminal(ctx context.Context, id string) (TerminalView, error) {
	var v TerminalView
	return v, c.call(ctx, "GET", site(id)+"/terminal", nil, &v)
}

// SetTerminal allows or forbids the terminal of a website.
func (c *Client) SetTerminal(ctx context.Context, id string, on bool) error {
	return c.call(ctx, "POST", site(id)+"/terminal", map[string]bool{"on": on}, nil)
}

// DialTerminal opens a terminal session of a website for a panel user. The connection lives as long as the
// session, so it does not use the client's request timeout.
func (c *Client) DialTerminal(ctx context.Context, id string, user int64, cols, rows int) (*websocket.Conn, error) {
	if c.hc == nil {
		return nil, ErrNoAgent
	}
	q := url.Values{"user": {fmt.Sprint(user)}, "cols": {fmt.Sprint(cols)}, "rows": {fmt.Sprint(rows)}}
	ws, resp, err := websocket.Dial(ctx, "ws://agent"+site(id)+"/terminal/session?"+q.Encode(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: c.hc.Transport}})
	if err != nil {
		if resp != nil && resp.StatusCode >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			if resp.Body != nil && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e) == nil && e.Error != "" {
				return nil, &Error{Code: resp.StatusCode, Msg: e.Error}
			}
			return nil, &Error{Code: resp.StatusCode, Msg: fmt.Sprintf("the agent refused the terminal (%d)", resp.StatusCode)}
		}
		return nil, err
	}
	return ws, nil
}
