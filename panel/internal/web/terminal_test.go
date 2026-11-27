package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// termSession is the agent's side of a session: it greets, echoes binary messages and reports what it got.
func (f *fakeAgent) termSession(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	on, seen := f.term.On, f.termSeen
	f.mu.Unlock()
	if !on {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "the terminal of this website is off"})
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	if seen != nil {
		seen <- r.URL.RawQuery
	}
	ctx := context.Background()
	_ = c.Write(ctx, websocket.MessageBinary, []byte("READY$ "))
	for {
		t, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if seen != nil {
			seen <- string(b)
		}
		if t == websocket.MessageBinary && string(b) == "exit\r" {
			_ = c.Close(websocket.StatusNormalClosure, "shell ended")
			return
		}
		if t == websocket.MessageBinary {
			_ = c.Write(ctx, websocket.MessageBinary, append([]byte("echo:"), b...))
		}
	}
}

func readTerm(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := ""
	for !strings.Contains(got, want) {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, got, err)
		}
		got += string(b)
	}
}

func TestTerminalPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.term = agent.TerminalView{Possible: true, Target: &agent.TermTarget{Service: "php", User: "33:33", Dir: "/var/www/html"}, Max: 2, IdleMin: 15, MaxMin: 120}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?done=on", "?done=off"} {
			if code, body, _ := e.get(e.c, "/terminal/voltix-id"+q); code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
				t.Errorf("%s %s: %d %v", lang, q, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, h := e.get(e.c, "/terminal/voltix-id")
	if !strings.Contains(body, `href="/terminal/voltix-id" aria-current="page"`) || !strings.Contains(body, "Turn the terminal on") || strings.Contains(body, "xterm.js") || strings.Contains(h.Get("Content-Security-Policy"), "nonce-") {
		t.Fatalf("off page:\n%s", body)
	}
	if code, _, hh := e.post(e.c, "/terminal/voltix-id", url.Values{"op": {"on"}}, "/terminal/voltix-id"); code != 303 || hh.Get("Location") != "/terminal/voltix-id?done=on" || f.termCalls[0] != `{"on":true}` {
		t.Fatalf("on: %d %v %v", code, hh.Get("Location"), f.termCalls)
	}
	_, body, h = e.get(e.c, "/terminal/voltix-id")
	m := regexp.MustCompile(`data-nonce="([^"]+)"`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(h.Get("Content-Security-Policy"), "'nonce-"+m[1]+"'") || !strings.Contains(body, "/static/vendor/xterm.js") || !strings.Contains(body, `data-socket="/terminal/voltix-id/socket"`) {
		t.Fatalf("on page:\n%s\n%s", h.Get("Content-Security-Policy"), body)
	}
	csrf := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(body)[1]
	wsURL := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/terminal/voltix-id/socket"
	dial := func(origin string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hd := http.Header{}
		if origin != "" {
			hd.Set("Origin", origin)
		}
		return websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: e.c, HTTPHeader: hd})
	}
	// another site's page cannot open it with the panel's cookie
	if _, resp, err := dial("https://evil.example"); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("cross origin: %v %v", resp, err)
	}
	// a wrong token is refused before the agent is asked
	f.termSeen = make(chan string, 16)
	c, _, err := dial(e.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"csrf":"wrong","cols":80,"rows":24}`))
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("wrong token: %v", err)
	}
	if len(f.termSeen) != 0 {
		t.Fatal("the agent was asked with a wrong token")
	}
	// a session: the size and the user go to the agent, the bytes both ways
	c, _, err = dial(e.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"csrf":"`+csrf+`","cols":120,"rows":33}`))
	readTerm(t, c, "READY$ ")
	if q := <-f.termSeen; !strings.Contains(q, "cols=120") || !strings.Contains(q, "rows=33") || !strings.Contains(q, "user=") {
		t.Fatalf("query %q", q)
	}
	_ = c.Write(ctx, websocket.MessageBinary, []byte("ls\r"))
	readTerm(t, c, "echo:ls\r")
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"cols":100,"rows":20}`))
	<-f.termSeen
	if got := <-f.termSeen; got != `{"cols":100,"rows":20}` {
		t.Fatalf("resize %q", got)
	}
	_ = c.Write(ctx, websocket.MessageBinary, []byte("exit\r"))
	<-f.termSeen
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure || !strings.Contains(err.Error(), "shell ended") {
		t.Fatalf("end %v", err)
	}
	var audit string
	for i := 0; i < 50 && !strings.Contains(audit, "site.terminal-close"); i++ {
		time.Sleep(20 * time.Millisecond)
		_, audit, _ = e.get(e.c, "/audit")
	}
	for _, want := range []string{"site.terminal", "site.terminal-open", "site.terminal-close", "bytes in", "shell ended"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	if strings.Contains(audit, "ls\r") || strings.Contains(audit, "echo:") {
		t.Fatal("typed text in the audit")
	}
	// off: the agent refuses the session; the browser gets the reason
	e.post(e.c, "/terminal/voltix-id", url.Values{"op": {"off"}}, "/terminal/voltix-id")
	c, _, err = dial(e.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"csrf":"`+csrf+`","cols":80,"rows":24}`))
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusTryAgainLater || !strings.Contains(err.Error(), "terminal of this website is off") {
		t.Fatalf("off: %v", err)
	}
	// a website without a shell: no tab, no button
	f.term = agent.TerminalView{Possible: false, Why: "static websites and ready apps have no shell to open"}
	f.app = "static"
	_, body, _ = e.get(e.c, "/terminal/voltix-id")
	if strings.Contains(body, `value="on"`) || !strings.Contains(body, "no shell to open") || strings.Contains(body, `href="/terminal/voltix-id"`) {
		t.Errorf("static:\n%s", body)
	}
}
