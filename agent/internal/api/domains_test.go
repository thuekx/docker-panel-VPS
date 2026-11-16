package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDomainRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	wait := func(job string) {
		for i := 0; i < 300; i++ {
			if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(job)
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/domains", ""); code != 200 || out["canonical"] == "" || len(out["hosts"].([]any)) == 0 {
		t.Fatalf("domains %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/domains", ""); code != 404 {
		t.Fatalf("unknown site: %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/domains", `{"op":"add","name":"bad name"}`); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/domains", `{"op":"add","name":"x.id","extra":1}`); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	code, out := do(t, c, "POST", "/v1/sites/voltix-id/domains", `{"op":"redirect-add","from":"/old","to":"/new","code":301}`)
	if code != 202 {
		t.Fatalf("redirect %d %v", code, out)
	}
	wait(out["job"].(string))
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/domains", `{"op":"refresh"}`); code != 200 || out["job"] != "" {
		t.Fatalf("no change %d %v", code, out)
	}
	bl, _ := os.ReadFile(logPath)
	for _, want := range []string{`"action":"domains","site":"voltix-id","result":"ok","detail":"redirect /old to /new (301) job `, `"action":"domains","site":"voltix-id","result":"refused","detail":"bad input: domain needs at least one dot"`} {
		if !strings.Contains(string(bl), want) {
			t.Errorf("audit lacks %s:\n%s", want, bl)
		}
	}
}
