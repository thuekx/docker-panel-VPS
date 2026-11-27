package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRedisRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
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
	wait(out["job"].(string))
	code, v := do(t, c, "GET", "/v1/sites/voltix-id/redis", "")
	if code != 200 || v["possible"] != true || v["conf"] != nil || v["kind"] != "wordpress" {
		t.Fatalf("status %d %v", code, v)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/redis", `{"on":true,"mode":"cache","mb":100}`); code != 400 {
		t.Fatalf("bad size %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/redis/flush", ""); code != 400 {
		t.Fatalf("flush while off %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/redis", ""); code != 404 {
		t.Fatalf("unknown %d", code)
	}
	code, j := do(t, c, "POST", "/v1/sites/voltix-id/redis", `{"on":true,"mode":"cache","mb":128}`)
	if code != 202 {
		t.Fatalf("on %d %v", code, j)
	}
	wait(j["job"].(string))
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), `"redis"`) && !strings.Contains(string(b), "redis") {
		t.Fatalf("audit %s", b)
	}
}
