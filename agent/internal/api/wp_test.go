package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestWPRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	for i := 0; i < 200; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, x := range []struct{ path, body string }{
		{"/v1/sites/voltix-id/wp/plugins", `{"do":"activate","names":["--require=/x.php"]}`},
		{"/v1/sites/voltix-id/wp/plugins", `{"do":"update","names":["a"],"shell":"x"}`},
		{"/v1/sites/voltix-id/wp/themes", `{"do":"delete","all":true}`},
		{"/v1/sites/voltix-id/wp/core", `{"do":"downgrade"}`},
		{"/v1/sites/voltix-id/wp/settings", `{"key":"siteurl","on":true}`},
		{"/v1/sites/voltix-id/wp/cli", `{"line":"shell"}`},
		{"/v1/sites/voltix-id/wp/cli", `{"line":"option get \"x"}`},
	} {
		if code, out := do(t, c, "POST", x.path, x.body); code != 400 {
			t.Errorf("%s %s answered %d %v", x.path, x.body, code, out)
		}
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nosuch/wp", ""); code != 404 {
		t.Errorf("unknown site answered %d", code)
	}
	// the fake docker gives no WP-CLI output: the call fails, and the audit still holds no argument
	do(t, c, "POST", "/v1/sites/voltix-id/wp/cli", `{"line":"user update 1 --user_pass=topsecret-xyz"}`)
	bl, _ := os.ReadFile(logPath)
	if strings.Contains(string(bl), "topsecret-xyz") {
		t.Error("audit holds console arguments")
	}
	if !strings.Contains(string(bl), `"action":"wp-cli","site":"voltix-id","result":"refused"`) {
		t.Error("audit lacks the console call")
	}
	if !strings.Contains(string(bl), `"action":"wp-plugin-activate","site":"voltix-id","result":"refused"`) {
		t.Error("audit lacks the refused plugin call")
	}
}
