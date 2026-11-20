package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestSecurityRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	wait := func(job string) {
		for i := 0; i < 300; i++ {
			if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(out["job"].(string))
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/security", ""); code != 200 || out["blocked"] == nil || out["hotlink_exts"] == nil {
		t.Fatalf("security %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/security", ""); code != 404 {
		t.Fatalf("unknown site: %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/security", `{"op":"block-add","ip":"0.0.0.0/0"}`); code != 400 {
		t.Fatalf("too wide: %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/security", `{"op":"block-add","ip":"1.2.3.4","x":1}`); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	hash := "$2y$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	code, out := do(t, c, "POST", "/v1/sites/voltix-id/security", `{"op":"protect-set","path":"/admin","user":"ana","hash":"`+hash+`"}`)
	if code != 202 {
		t.Fatalf("protect %d %v", code, out)
	}
	wait(out["job"].(string))
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/security", `{"op":"hotlink-off"}`); code != 200 || out["job"] != "" {
		t.Fatalf("no change %d %v", code, out)
	}
	bl, _ := os.ReadFile(logPath)
	if !strings.Contains(string(bl), `"action":"security","site":"voltix-id","result":"ok","detail":"protect-set /admin user ana job `) {
		t.Errorf("audit lacks the change:\n%s", bl)
	}
	if strings.Contains(string(bl), "$2y$") || strings.Contains(string(bl), "abcdefghijklmnop") {
		t.Errorf("audit holds the hash:\n%s", bl)
	}
}
