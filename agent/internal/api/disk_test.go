package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiskRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, cr := do(t, c, "POST", "/v1/sites", `{"app":"static","domain":"voltix.id","aliases":[],"primary":"domain","server":"nginx","php":"","admin_email":"","title":""}`)
	job, _ := cr["job"].(string)
	for i := 0; i < 300 && job != ""; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, out := do(t, c, "GET", "/v1/sites/voltix-id/disk?path=/", "")
	if rows, _ := out["rows"].([]any); code != 200 || len(rows) == 0 || out["path"] != "/" {
		t.Fatalf("site disk %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/disk?path=/&x=1", ""); code != 400 {
		t.Errorf("unknown query key %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/disk", ""); code != 404 {
		t.Errorf("unknown site %d", code)
	}
	if code, out := do(t, c, "GET", "/v1/disk", ""); code != 200 || out["sites"] == nil {
		t.Errorf("server disk %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/disk/clean", `{"what":"volumes"}`); code != 400 {
		t.Errorf("volumes clean-up %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/disk/clean", `{"what":"build-cache"}`); code != 200 {
		t.Errorf("build cache %d %v", code, out)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "disk-clean") {
		t.Errorf("audit log %s", b)
	}
}
