package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestImageRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, cr := do(t, c, "POST", "/v1/sites", `{"app":"static","domain":"voltix.id","aliases":[],"primary":"domain","server":"nginx","php":"","admin_email":"","title":""}`)
	job, _ := cr["job"].(string)
	for i := 0; i < 300 && job != ""; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, out := do(t, c, "GET", "/v1/sites/voltix-id/images", "")
	svcs, _ := out["services"].([]any)
	if code != 200 || out["update"] != false || len(svcs) != 1 {
		t.Fatalf("images %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/images/update", ""); code != 400 || !strings.Contains(out["error"].(string), "newest images") {
		t.Errorf("update with nothing to do %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/images", ""); code != 404 {
		t.Errorf("unknown site %d", code)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "image-update") {
		t.Errorf("audit log %s", b)
	}
}
