package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDeployKeyRoutes(t *testing.T) {
	c, _ := serve(t, map[int]bool{os.Getuid(): true})
	code, out := do(t, c, "POST", "/v1/deploykeys", "")
	if pub, _ := out["public"].(string); code != 200 || !strings.HasPrefix(pub, "ssh-ed25519 ") || out["id"] == "" {
		t.Fatalf("new key %d %v", code, out)
	}
	for k := range out {
		if k != "id" && k != "public" && k != "fingerprint" {
			t.Errorf("new key answers %q", k)
		}
	}
	_, cr := do(t, c, "POST", "/v1/sites", good)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+cr["job"].(string), ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/deploykey", ""); code != 404 {
		t.Errorf("WordPress site key answered %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/deploykey", ""); code != 404 {
		t.Errorf("unknown site key answered %d", code)
	}
}
