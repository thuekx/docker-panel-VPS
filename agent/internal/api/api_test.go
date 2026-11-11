package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/audit"
	"github.com/thuekx/docker-panel-VPS/agent/internal/sites"
)

type nopRunner struct{}

func (nopRunner) Docker(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	if stdin != nil {
		_, _ = io.ReadAll(stdin)
	}
	return nil, nil
}

const ip = "203.0.113.10"

// serve starts the API on a real Unix socket so the peer check runs as in production.
func serve(t *testing.T, allowed map[int]bool) (*http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	m := sites.New(sites.Config{Data: dir, PublicIPv4: ip, PanelDomain: "panel.nostek.dev", PreviewZone: "preview.nostek.dev"},
		nopRunner{}, func(ctx context.Context, h string) ([]string, error) {
			if strings.HasSuffix(h, ".preview.nostek.dev") {
				return []string{ip}, nil
			}
			return nil, errors.New("nx")
		})
	logPath := filepath.Join(dir, "audit.log")
	s := &Server{M: m, Audit: audit.Open(logPath), Allowed: allowed, Version: "test"}
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler(), ConnContext: ConnContext}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("unix", sock) },
	}}
	return c, logPath
}

func do(t *testing.T, c *http.Client, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r, _ := http.NewRequest(method, "http://agent"+path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const good = `{"domain":"voltix.id","aliases":[],"primary":"domain","server":"nginx","php":"8.4","admin_email":"owner@nostek.dev","title":"Voltix"}`

func TestPeerRefused(t *testing.T) {
	c, logPath := serve(t, map[int]bool{424242: true})
	code, _ := do(t, c, "GET", "/v1/health", "")
	if code != 403 {
		t.Fatalf("code = %d, want 403 for a uid not in the list", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), `"result":"refused"`) {
		t.Error("refusal not audited")
	}
}

func TestOnlyFixedRoutes(t *testing.T) {
	c, _ := serve(t, map[int]bool{os.Getuid(): true})
	if code, out := do(t, c, "GET", "/v1/health", ""); code != 200 || out["ok"] != true {
		t.Fatalf("health = %d %v", code, out)
	}
	for _, p := range []string{"/v1/containers/create", "/v1/exec", "/v1/sites/x/exec", "/", "/v2/sites"} {
		if code, _ := do(t, c, "POST", p, `{}`); code != 404 && code != 405 {
			t.Errorf("%s answered %d", p, code)
		}
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/rm", `{}`); code != 404 {
		t.Errorf("unknown action answered %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites?x=1", ""); code != 400 {
		t.Errorf("query on list answered %d", code)
	}
}

func TestCreateStrictBodyAndSecretOnce(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	bad := map[string]string{
		"unknown field": strings.Replace(good, `"title"`, `"image":"evil","title"`, 1),
		"two bodies":    good + good,
		"not json":      "domain=voltix.id",
		"too big":       `{"domain":"` + strings.Repeat("a", MaxBody) + `"}`,
	}
	for name, body := range bad {
		if code, _ := do(t, c, "POST", "/v1/sites", body); code != 400 {
			t.Errorf("%s: code %d, want 400", name, code)
		}
	}
	if code, out := do(t, c, "POST", "/v1/sites", strings.Replace(good, `"nginx"`, `"caddy"`, 1)); code != 422 {
		t.Errorf("bad server: %d %v", code, out)
	}
	code, plan := do(t, c, "POST", "/v1/sites/plan", good)
	if code != 200 || plan["url"] != "https://voltix-id.preview.nostek.dev" {
		t.Fatalf("plan = %d %v", code, plan)
	}
	code, out := do(t, c, "POST", "/v1/sites", good)
	if code != 202 {
		t.Fatalf("create = %d %v", code, out)
	}
	job := out["job"].(string)
	var st map[string]any
	for i := 0; i < 200; i++ {
		_, st = do(t, c, "GET", "/v1/jobs/"+job, "")
		if st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st["state"] != "done" || st["has_secret"] != true {
		t.Fatalf("job = %v", st)
	}
	code, sec := do(t, c, "POST", "/v1/jobs/"+job+"/secret", "")
	pw, _ := sec["password"].(string)
	if code != 200 || len(pw) != 24 {
		t.Fatalf("secret = %d %v", code, sec)
	}
	if code, _ := do(t, c, "POST", "/v1/jobs/"+job+"/secret", ""); code != 410 {
		t.Errorf("second secret read answered %d, want 410", code)
	}
	if code, out := do(t, c, "GET", "/v1/sites", ""); code != 200 || len(out["sites"].([]any)) != 1 {
		t.Errorf("list = %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/logs?service=php&lines=50&cmd=x", ""); code != 400 {
		t.Errorf("unknown log parameter answered %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/logs?service=php&lines=50", ""); code != 200 {
		t.Errorf("logs answered %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/delete", ""); code != 200 || out["days"] != float64(7) {
		t.Errorf("delete = %d %v", code, out)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), pw) {
		t.Fatal("audit log holds the admin password")
	}
	for _, want := range []string{`"action":"create"`, `"action":"secret"`, `"action":"delete"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
}
