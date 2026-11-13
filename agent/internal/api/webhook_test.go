package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWebhookRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, cr := do(t, c, "POST", "/v1/sites", `{"app":"node","domain":"voltix.id","aliases":[],"primary":"domain","server":"","php":"","admin_email":"","title":"","version":"24","repo":"https://github.com/owner/shop.git","branch":"main"}`)
	job, _ := cr["job"].(string)
	for i := 0; i < 300 && job != ""; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ping := `{"zen":"hi"}`
	deliver := func(sig string) (int, map[string]any) {
		b := `{"event":"ping","signature":"` + sig + `","body":"` + base64.StdEncoding.EncodeToString([]byte(ping)) + `"}`
		return do(t, c, "POST", "/v1/sites/voltix-id/webhook/deliver", b)
	}
	if code, _ := deliver("sha256=00"); code != 404 {
		t.Errorf("delivery while off: %d", code)
	}
	code, out := do(t, c, "POST", "/v1/sites/voltix-id/webhook/secret", "")
	sec, _ := out["secret"].(string)
	if code != 200 || len(sec) != 64 {
		t.Fatalf("secret %d %v", code, out)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/webhook", ""); code != 200 || out["on"] != true || out["branch"] != "main" || out["secret"] != nil {
		t.Fatalf("info %d %v", code, out)
	}
	if code, _ := deliver("sha256=00"); code != 401 {
		t.Errorf("bad signature: %d", code)
	}
	mac := hmac.New(sha256.New, []byte(sec))
	mac.Write([]byte(ping))
	if code, out := deliver("sha256=" + hex.EncodeToString(mac.Sum(nil))); code != 200 || out["result"] != "ping" {
		t.Errorf("signed ping %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/nope/webhook/deliver", `{"event":"ping","body":""}`); code != 404 {
		t.Errorf("unknown site: %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/webhook/remove", ""); code != 200 {
		t.Errorf("remove %d", code)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/webhook", ""); code != 200 || out["on"] != false {
		t.Errorf("after remove %d %v", code, out)
	}
	if b, _ := os.ReadFile(logPath); strings.Contains(string(b), sec) || !strings.Contains(string(b), "webhook-secret") || !strings.Contains(string(b), "bad signature") {
		t.Errorf("audit log %s", b)
	}
}
