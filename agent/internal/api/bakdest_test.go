package api

import (
	"os"
	"strings"
	"testing"
)

func TestBackupPlanRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	const pass = "a long schedule passphrase"
	if code, _ := do(t, c, "POST", "/v1/backup-plan/schedule", `{"every":"daily","hour":2}`); code != 400 {
		t.Fatalf("on without passphrase %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/backup-plan/schedule", `{"every":"daily","hour":2,"passphrase":"`+pass+`"}`); code != 200 {
		t.Fatalf("schedule %d %v", code, out)
	}
	code, out := do(t, c, "POST", "/v1/backup-plan/dests", `{"kind":"s3","name":"S3","on":true,"keep":7,"endpoint":"https://is3.example.net","bucket":"nostek-bk","prefix":"srv","access_key":"AKTEST","secret":"top-secret-key"}`)
	if code != 200 {
		t.Fatalf("dest %d %v", code, out)
	}
	id := out["id"].(string)
	if code, _ := do(t, c, "POST", "/v1/backup-plan/dests", `{"kind":"s3","name":"S3","keep":7,"endpoint":"http://x","bucket":"nostek-bk","access_key":"AK","secret":"x"}`); code != 400 {
		t.Fatalf("bad dest %d", code)
	}
	code, plan := do(t, c, "GET", "/v1/backup-plan", "")
	if code != 200 || plan["every"] != "daily" || plan["sealed"] != true {
		t.Fatalf("plan %d %v", code, plan)
	}
	ds := plan["dests"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["has_secret"] != true {
		t.Fatalf("dests %v", ds)
	}
	if code, _ := do(t, c, "POST", "/v1/backup-plan/run", `{}`); code != 400 {
		t.Fatalf("run when not due / no copy %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/server-backups/nostek-server-20261101-020000.npbk/send", `{"dest":"`+id+`"}`); code != 404 {
		t.Fatalf("send missing %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/backup-plan/dests/"+id+"/delete", ""); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/backup-plan/dests/"+id+"/delete", ""); code != 404 {
		t.Fatalf("delete again %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), pass) || strings.Contains(string(b), "top-secret-key") {
		t.Fatal("a secret is in the audit log")
	}
	for _, want := range []string{"backup-schedule", "daily 2:00 new key", "backup-dest"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}
