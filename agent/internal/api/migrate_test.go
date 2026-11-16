package api

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMigrateRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/migrate/upload?part=archive", "text/plain", "x"); code != 400 {
		t.Fatalf("content type: %d", code)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/migrate/upload?part=archive&x=1", "application/octet-stream", "x"); code != 400 {
		t.Fatalf("odd parameter: %d", code)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/migrate/upload?part=site.json", "application/octet-stream", "x"); code != 400 {
		t.Fatalf("odd part: %d", code)
	}
	if code, body, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/migrate/upload?part=archive", "application/octet-stream", "not a backup"); code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/migrate", ""); code != 200 || out["archive"] != float64(12) {
		t.Fatalf("status %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/migrate", ""); code != 404 {
		t.Fatalf("unknown site: %d", code)
	}
	key := strings.Repeat("s", 43)
	code := "nm1." + base64.RawURLEncoding.EncodeToString([]byte(`{"u":"https://127.0.0.1","k":"`+key+`"}`))
	if c, out := do(t, c, "POST", "/v1/sites/voltix-id/migrate", `{"way":"plugin","code":"`+code+`"}`); c != 202 {
		t.Fatalf("start %d %v", c, out)
	}
	if c, _ := do(t, c, "POST", "/v1/sites/voltix-id/migrate", `{"way":"ssh"}`); c != 400 && c != 409 {
		t.Fatalf("odd way: %d", c)
	}
	if c, _ := do(t, c, "POST", "/v1/sites/voltix-id/migrate", `{"way":"plugin","code":"x","extra":1}`); c != 400 {
		t.Fatalf("unknown field: %d", c)
	}
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 300; i++ {
		if c, _ := do(t, c, "POST", "/v1/sites/voltix-id/migrate/clear", ""); c == 200 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	bl, _ := os.ReadFile(logPath)
	for _, want := range []string{`"action":"migrate-upload","site":"voltix-id","result":"ok","detail":"archive 0 MB"`, `"action":"migrate","site":"voltix-id","result":"ok","detail":"plugin from 127.0.0.1 job `, `"action":"migrate-clear"`} {
		if !strings.Contains(string(bl), want) {
			t.Errorf("audit lacks %s:\n%s", want, bl)
		}
	}
	if strings.Contains(string(bl), key) || strings.Contains(string(bl), code) {
		t.Error("audit holds the migration key")
	}
}
