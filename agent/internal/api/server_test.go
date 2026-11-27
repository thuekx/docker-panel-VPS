package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestServerRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	if code, out := do(t, c, "GET", "/v1/server/services", ""); code != 200 || out["services"] == nil {
		t.Fatalf("services %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/server/services/docker/restart", ""); code != 400 {
		t.Fatalf("docker restart %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/server/services/Bad..Name/restart", ""); code != 400 {
		t.Fatalf("bad name %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/server/services/unknown/restart", ""); code != 404 {
		t.Fatalf("unknown %d", code)
	}
	_, out := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+out["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, d := do(t, c, "GET", "/v1/server/drift", "")
	if code != 200 || len(d["sites"].([]any)) != 1 {
		t.Fatalf("drift %d %v", code, d)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/drift/fix", ""); code != 200 {
		t.Fatalf("fix %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/nope/drift/fix", ""); code != 404 {
		t.Fatalf("fix unknown %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "drift-fix") || !strings.Contains(string(b), "core-restart") {
		t.Fatalf("audit %s", b)
	}
}
