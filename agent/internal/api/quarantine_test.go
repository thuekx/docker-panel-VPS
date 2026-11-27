package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuarantineRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	data := filepath.Dir(logPath)
	_, out := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+out["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/security/scan-schedule", `{"every":"hourly"}`); code != 400 {
		t.Fatalf("bad schedule %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/security/scan-schedule", `{"every":"weekly","quarantine":true}`); code != 200 || out["scan_schedule"] == nil {
		t.Fatalf("schedule %d %v", code, out)
	}
	wp := filepath.Join(data, "sites", "voltix-id", "wp")
	os.MkdirAll(wp, 0o755)
	os.WriteFile(filepath.Join(wp, "evil.php"), []byte("<?php"), 0o644)
	code, q := do(t, c, "POST", "/v1/sites/voltix-id/quarantine", `{"path":"/evil.php","signature":"Php.Test"}`)
	if code != 200 || q["path"] != "/evil.php" {
		t.Fatalf("quarantine %d %v", code, q)
	}
	_, list := do(t, c, "GET", "/v1/quarantine", "")
	if files, _ := list["files"].([]any); len(files) != 1 {
		t.Fatalf("list %v", list)
	}
	qid := q["id"].(string)
	if code, _ := do(t, c, "POST", "/v1/quarantine/"+qid+"/restore", ""); code != 200 {
		t.Fatalf("restore %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/quarantine/"+qid+"/delete", ""); code != 404 {
		t.Fatalf("delete after restore %d", code)
	}
	b, _ := os.ReadFile(logPath)
	for _, want := range []string{"scan-schedule", "weekly, quarantine", "quarantine-restore"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}
