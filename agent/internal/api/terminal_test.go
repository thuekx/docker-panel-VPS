package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var testTermIdle time.Duration // TERM-01: set by a test before serveM

// wsDial opens a terminal session over the agent's socket.
func wsDial(t *testing.T, sock, path string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("unix", sock) }}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws://agent"+path, &websocket.DialOptions{HTTPClient: hc})
}

// readUntil reads terminal output until it holds want.
func readUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, got.String(), err)
		}
		got.Write(b)
	}
	return got.String()
}

func TestTerminalRoutes(t *testing.T) {
	testTermIdle = 300 * time.Millisecond
	t.Cleanup(func() { testTermIdle = 0 })
	c, logPath, m := serveM(t, map[int]bool{os.Getuid(): true})
	sock := filepath.Join(filepath.Dir(logPath), "a.sock")
	_, out := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+out["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, v := do(t, c, "GET", "/v1/sites/voltix-id/terminal", ""); code != 200 || v["possible"] != true || v["on"] != false {
		t.Fatalf("get %d %v", code, v)
	}
	if _, resp, err := wsDial(t, sock, "/v1/sites/voltix-id/terminal/session?user=7"); err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("session while off: %v %v", resp, err)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/terminal", `{"on":true}`); code != 200 {
		t.Fatalf("on %d", code)
	}
	if _, resp, err := wsDial(t, sock, "/v1/sites/voltix-id/terminal/session?user=7&x=1"); err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("unknown query key: %v %v", resp, err)
	}
	m.UseTermCmd(func(ctx context.Context, dir string, args []string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `stty -echo; echo READY; while read l; do echo "got:$l"; stty size; done`)
	})
	ws, _, err := wsDial(t, sock, "/v1/sites/voltix-id/terminal/session?user=7&cols=90&rows=20")
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, ws, "READY")
	ctx := context.Background()
	_ = ws.Write(ctx, websocket.MessageText, []byte(`{"cols":120,"rows":40}`))
	time.Sleep(50 * time.Millisecond)
	_ = ws.Write(ctx, websocket.MessageBinary, []byte("hello\n"))
	if got := readUntil(t, ws, "40 120"); !strings.Contains(got, "got:hello") {
		t.Fatalf("echo %q", got)
	}
	if v := m; v != nil {
		if tv, _ := m.Terminal("voltix-id"); tv.Open != 1 {
			t.Fatalf("open %d", tv.Open)
		}
	}
	// idle: the agent ends the session
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		if _, _, err := ws.Read(rctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure || !strings.Contains(err.Error(), "idle") {
				t.Fatalf("end %v", err)
			}
			break
		}
	}
	for i := 0; i < 100; i++ {
		if tv, _ := m.Terminal("voltix-id"); tv.Open == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tv, _ := m.Terminal("voltix-id"); tv.Open != 0 {
		t.Fatal("session still counted")
	}
	b, _ := os.ReadFile(logPath)
	for _, want := range []string{"terminal-set", "open by user 7 in", "terminal-end", "user 7, ", "idle"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	if strings.Contains(string(b), "hello") {
		t.Fatal("typed text in the audit")
	}
}
