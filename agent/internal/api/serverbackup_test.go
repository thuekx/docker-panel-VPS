package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerBackupRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	data := filepath.Dir(logPath)
	wait := func(job string) map[string]any {
		var st map[string]any
		for i := 0; i < 300; i++ {
			if _, st = do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return st
	}
	_, out := do(t, c, "POST", "/v1/sites", good)
	wait(out["job"].(string))

	const pass = "server passphrase 42"
	if code, out := do(t, c, "POST", "/v1/server-backups", `{"passphrase":"`+pass+`","panel_version":"v1.34.0"}`); code != 400 || !strings.Contains(out["error"].(string), "panel's copy") {
		t.Fatalf("without the panel's copy: %d %v", code, out)
	}
	_ = os.MkdirAll(filepath.Join(data, "panel"), 0o700)
	_ = os.WriteFile(filepath.Join(data, "panel", "server-backup.db"), []byte("SQLite format 3"), 0o600)
	for _, body := range []string{`{"passphrase":"short"}`, `{"passphrase":"` + pass + `","x":1}`, `{"passphrase":"` + pass + `","panel_version":"` + strings.Repeat("v", 41) + `"}`} {
		if code, out := do(t, c, "POST", "/v1/server-backups", body); code != 400 {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	code, out := do(t, c, "POST", "/v1/server-backups", `{"passphrase":"`+pass+`","panel_version":"v1.34.0"}`)
	if code != 202 {
		t.Fatalf("start %d %v", code, out)
	}
	if st := wait(out["job"].(string)); st["state"] != "done" {
		t.Fatalf("job %v", st)
	}
	code, out = do(t, c, "GET", "/v1/server-backups", "")
	list, _ := out["backups"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("list %d %v", code, out)
	}
	name := list[0].(map[string]any)["name"].(string)
	code, body, h := raw(t, c, "GET", "/v1/server-backups/"+name, "", "")
	if code != 200 || !strings.HasPrefix(body, "NPBK1\n") || !strings.Contains(h.Get("Content-Disposition"), name) {
		t.Fatalf("download %d %v", code, h)
	}
	for _, x := range []struct{ method, path string }{
		{"GET", "/v1/server-backups/..%2Fpanel%2Fpanel.db"},
		{"GET", "/v1/server-backups/nostek-server-20260101-000000.npbk"},
		{"POST", "/v1/server-backups/x.npbk/delete"},
	} {
		if code, _, _ := raw(t, c, x.method, x.path, "", ""); code != 404 {
			t.Errorf("%s %s: %d", x.method, x.path, code)
		}
	}
	if code, _ := do(t, c, "POST", "/v1/server-backups/"+name+"/delete", ""); code != 200 {
		t.Fatalf("delete %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "server-backup") || strings.Contains(string(b), pass) {
		t.Fatalf("audit log:\n%s", b)
	}
}
