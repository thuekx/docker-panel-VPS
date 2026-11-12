package api

import (
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBackupRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
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
	_, out := do(t, c, "POST", "/v1/sites", good)
	wait(out["job"].(string))

	code, out := do(t, c, "POST", "/v1/sites/voltix-id/backups", "")
	if code != 202 {
		t.Fatalf("backup answered %d %v", code, out)
	}
	if st := wait(out["job"].(string)); st["state"] != "done" {
		t.Fatalf("backup job %v", st)
	}
	code, out = do(t, c, "GET", "/v1/sites/voltix-id/backups", "")
	list, _ := out["backups"].([]any)
	if code != 200 || len(list) != 1 || out["keep"] != float64(7) {
		t.Fatalf("list %d %v", code, out)
	}
	name := list[0].(map[string]any)["name"].(string)

	code, body, h := raw(t, c, "GET", "/v1/sites/voltix-id/backups/"+name, "", "")
	if code != 200 || !strings.Contains(h.Get("Content-Disposition"), "voltix-id-"+name) {
		t.Fatalf("download %d %v", code, h)
	}
	if zr, err := gzip.NewReader(strings.NewReader(body)); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(zr); !strings.Contains(string(b), "-- dump") {
		t.Fatal("download lacks the database")
	}

	for _, x := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/v1/sites/voltix-id/backups/schedule", `{"daily":true,"keep":31}`, 400},
		{"POST", "/v1/sites/voltix-id/backups/schedule", `{"daily":true,"keep":3,"hour":1}`, 400},
		{"POST", "/v1/sites/voltix-id/backups/" + name + "/restore", `{"what":"everything"}`, 400},
		{"POST", "/v1/sites/voltix-id/backups/..%2Fsite.json/restore", `{"what":"all"}`, 400},
		{"GET", "/v1/sites/voltix-id/backups/site.json", "", 400},
		{"GET", "/v1/sites/nosuch/backups", "", 404},
		{"POST", "/v1/sites/nosuch/backups", "", 404},
		{"POST", "/v1/sites/voltix-id/backups/manual-20200101-000000.tar.gz/delete", "", 404},
		{"POST", "/v1/sites/voltix-id/backups/schedule", `{"daily":true,"keep":3}`, 200},
	} {
		if code, out := do(t, c, x.method, x.path, x.body); code != x.want {
			t.Errorf("%s %s %s answered %d %v, want %d", x.method, x.path, x.body, code, out, x.want)
		}
	}
	if _, out := do(t, c, "GET", "/v1/sites/voltix-id/backups", ""); out["daily"] != true || out["keep"] != float64(3) {
		t.Errorf("schedule not kept: %v", out)
	}

	code, out = do(t, c, "POST", "/v1/sites/voltix-id/backups/"+name+"/restore", `{"what":"database"}`)
	if code != 202 {
		t.Fatalf("restore answered %d %v", code, out)
	}
	if st := wait(out["job"].(string)); st["state"] != "done" {
		t.Fatalf("restore job %v", st)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/backups/"+name+"/delete", ""); code != 200 {
		t.Fatalf("delete answered %d", code)
	}
	bl, _ := os.ReadFile(logPath)
	for _, want := range []string{`"action":"backup","site":"voltix-id","result":"ok"`, `"action":"backup-download"`, `"action":"backup-restore","site":"voltix-id","result":"ok","detail":"` + name + ` database`, `"action":"backup-delete"`, `"action":"backup-schedule","site":"voltix-id","result":"ok","detail":"daily on keep 3"`} {
		if !strings.Contains(string(bl), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	if strings.Contains(string(bl), "-- dump") {
		t.Error("audit holds backup content")
	}
}
