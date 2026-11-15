package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLimitRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, cr := do(t, c, "POST", "/v1/sites", `{"app":"php","domain":"voltix.id","aliases":[],"primary":"domain","server":"nginx","php":"8.4","admin_email":"","title":""}`)
	job, _ := cr["job"].(string)
	for i := 0; i < 300 && job != ""; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, out := do(t, c, "GET", "/v1/sites/voltix-id/limits", "")
	if svcs, _ := out["services"].([]any); code != 200 || len(svcs) != 2 || out["php"] == nil {
		t.Fatalf("limits %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/limits", `{"ram":{"php":1024}}`); code != 200 {
		t.Errorf("raise %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/limits", `{"ram":{"php":256}}`); code != 400 || !strings.Contains(out["error"].(string), "standard") {
		t.Errorf("below the standard %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/limits", `{"ram":{"php":1024},"extra":1}`); code != 400 {
		t.Errorf("unknown field %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/limits", ""); code != 404 {
		t.Errorf("unknown site %d", code)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "php 1024 MB") {
		t.Errorf("audit log %s", b)
	}
}
