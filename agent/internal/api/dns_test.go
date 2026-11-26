package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDNSRoutes(t *testing.T) {
	c, logPath, m := serveM(t, map[int]bool{os.Getuid(): true})
	cloudns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("sub-auth-id") != "42" || r.PostForm.Get("auth-password") != "pw-Secret-9" {
			_, _ = w.Write([]byte(`{"status":"Failed","statusDescription":"Invalid authentication"}`))
			return
		}
		if r.URL.Path == "/dns/get-zone-info.json" && r.PostForm.Get("domain-name") != "voltix.id" {
			_, _ = w.Write([]byte(`{"status":"Failed","statusDescription":"Missing domain-name"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"Success"}`))
	}))
	defer cloudns.Close()
	m.UseClouDNS(cloudns.URL)

	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	wait := func(job string) map[string]any {
		for i := 0; i < 300; i++ {
			if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
				return st
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("job did not finish")
		return nil
	}
	wait(job)

	if code, out := do(t, c, "GET", "/v1/dns", ""); code != 200 || out["configured"] != false {
		t.Fatalf("no provider %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/dns", `{"provider":"cloudflare","sub_auth_id":"42","password":"x"}`); code != 400 {
		t.Fatalf("other provider %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/dns", `{"provider":"cloudns","sub_auth_id":"42","password":"nope-Wrong"}`); code != 400 || !strings.Contains(out["error"].(string), "Invalid authentication") {
		t.Fatalf("bad password %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/wildcard", `{"zone":"voltix.id"}`); code != 400 {
		t.Fatalf("wildcard without a provider %d %v", code, out)
	}
	code, out := do(t, c, "POST", "/v1/dns", `{"provider":"cloudns","sub_auth_id":"42","password":"pw-Secret-9"}`)
	if code != 200 || out["configured"] != true || out["sub_auth_id"] != "42" {
		t.Fatalf("set %d %v", code, out)
	}
	_, text, _ := raw(t, c, "GET", "/v1/dns", "", "")
	if strings.Contains(text, "pw-Secret") {
		t.Fatal("the password is in an answer")
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/wildcard", `{"zone":"other.org"}`); code != 400 {
		t.Fatalf("foreign zone %d %v", code, out)
	}
	// voltix.id has no DNS here, so the website still lives on its preview name: no zone of its own yet
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/wildcard", `{"zone":"voltix.id"}`); code != 400 || !strings.Contains(out["error"].(string), "not a zone") {
		t.Fatalf("preview-only website %d %v", code, out)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/domains", ""); code != 200 || out["dns_provider"] != true || out["wildcard_zones"] != nil {
		t.Fatalf("domains %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/dns/delete", ""); code != 200 {
		t.Fatalf("remove %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "dns-set") || strings.Contains(string(b), "pw-Secret") || strings.Contains(string(b), "nope-Wrong") {
		t.Fatalf("audit:\n%s", b)
	}
}
