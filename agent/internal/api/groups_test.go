package api

import (
	"os"
	"testing"
	"time"
)

func TestGroupRoutes(t *testing.T) {
	c, _ := serve(t, map[int]bool{os.Getuid(): true})
	_, cr := do(t, c, "POST", "/v1/sites", `{"app":"php","domain":"voltix.id","aliases":[],"primary":"domain","server":"nginx","php":"8.4","admin_email":"","title":""}`)
	job, _ := cr["job"].(string)
	for i := 0; i < 300 && job != ""; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, out := do(t, c, "GET", "/v1/groups", ""); code != 200 || out["groups"] == nil {
		t.Fatalf("groups %d %v", code, out)
	}
	for _, body := range []string{`{"cpu_percent":5,"memory_mb":512,"tasks":256}`, `{"cpu_percent":100,"memory_mb":512,"tasks":256,"extra":1}`} {
		if code, out := do(t, c, "PUT", "/v1/groups/a1", body); code != 400 {
			t.Errorf("bad limits %s: %d %v", body, code, out)
		}
	}
	if code, _ := do(t, c, "PUT", "/v1/groups/Bad-Name", `{"cpu_percent":100,"memory_mb":512,"tasks":256}`); code != 400 {
		t.Errorf("bad name %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/group", `{"group":"none"}`); code != 400 {
		t.Errorf("unknown group %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/nope/group", `{"group":""}`); code != 404 {
		t.Errorf("unknown site %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/groups/none/delete", ""); code != 404 {
		t.Errorf("delete unknown %d", code)
	}
}
