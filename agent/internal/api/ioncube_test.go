package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestIonCubeRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+out["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, v := do(t, c, "GET", "/v1/sites/voltix-id/ioncube", ""); code != 200 || v["possible"] != true || v["on"] != false {
		t.Fatalf("get %d %v", code, v)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/ioncube", `{"on":false}`); code != 400 {
		t.Fatalf("off while off %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/ioncube", `{"on":true,"x":1}`); code != 400 {
		t.Fatalf("unknown field %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nosuch/ioncube", ""); code != 404 {
		t.Fatalf("unknown site %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "ioncube") {
		t.Errorf("audit lacks the refused change")
	}
}
