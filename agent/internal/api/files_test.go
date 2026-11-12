package api

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func raw(t *testing.T, c *http.Client, method, path, ctype, body string) (int, string, http.Header) {
	t.Helper()
	r, _ := http.NewRequest(method, "http://agent"+path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestFileRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	for i := 0; i < 200; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	const bin = "application/octet-stream"
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/files/content?path=/a.txt", "application/json", "x"); code != 400 {
		t.Errorf("upload with JSON type answered %d", code)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/files/content?path=/a.txt&cmd=rm", bin, "x"); code != 400 {
		t.Errorf("unknown parameter answered %d", code)
	}
	if code, b, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/files/content?path=/a.txt", bin, "hello"); code != 200 || !strings.Contains(b, `"size":5`) {
		t.Fatalf("upload = %d %s", code, b)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/files/content?path=/a.txt", bin, "again"); code != 409 {
		t.Errorf("second upload without overwrite answered %d", code)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/files/content?path=/a.txt&overwrite=1", bin, "hello2"); code != 200 {
		t.Errorf("overwrite answered %d", code)
	}
	code, b, h := raw(t, c, "GET", "/v1/sites/voltix-id/files/content?path=/a.txt", "", "")
	if code != 200 || b != "hello2" || h.Get("Content-Type") != bin || !strings.Contains(h.Get("Content-Disposition"), `"a.txt"`) {
		t.Fatalf("download = %d %q %v", code, b, h)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/files?path=/", ""); code != 200 || len(out["entries"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/files?path=/nope", ""); code != 400 {
		t.Errorf("list of a missing folder answered %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/nope/files?path=/", ""); code != 404 {
		t.Errorf("unknown site answered %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/mkdir", `{"path":"/d"}`); code != 200 {
		t.Errorf("mkdir answered %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/mkdir", `{"path":"/d"}`); code != 409 {
		t.Errorf("mkdir twice answered %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/rename", `{"path":"/a.txt","to":"/d/b.txt"}`); code != 200 {
		t.Errorf("rename answered %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/chmod", `{"path":"/d/b.txt","mode":"4755"}`); code != 400 {
		t.Errorf("setuid chmod answered %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/chmod", `{"path":"/d","mode":"750","x":1}`); code != 400 {
		t.Errorf("unknown field answered %d", code)
	}
	code, b, h = raw(t, c, "GET", "/v1/sites/voltix-id/files/zip?path=/d", "", "")
	if code != 200 || h.Get("Content-Type") != "application/zip" || !strings.HasPrefix(b, "PK") {
		t.Fatalf("zip = %d %v", code, h)
	}
	if code, _, _ := raw(t, c, "GET", "/v1/sites/voltix-id/files/zip?path=/missing", "", ""); code != 400 {
		t.Errorf("zip of a missing folder answered %d", code)
	}
	code, out = do(t, c, "POST", "/v1/sites/voltix-id/files/delete", `{"path":"/d"}`)
	if code != 200 || out["path"] != "/d" {
		t.Fatalf("delete = %d %v", code, out)
	}
	entry := out["entry"].(string)
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/files/trash", ""); code != 200 || len(out["trash"].([]any)) != 1 {
		t.Fatalf("trash = %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/files/trash/"+entry+"/restore", ""); code != 200 || out["path"] != "/d" {
		t.Fatalf("restore = %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/files/delete", `{"path":"/"}`); code != 400 {
		t.Errorf("delete of / answered %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/db/query", `{"sql":""}`); code != 400 {
		t.Errorf("empty SQL answered %d %v", code, out)
	}
	if code, out := do(t, c, "POST", "/v1/sites/voltix-id/db/query", `{"sql":"UPDATE wp_users SET user_pass = 'topsecret-xyz'"}`); code != 200 {
		t.Errorf("query answered %d %v", code, out)
	}
	if code, _, _ := raw(t, c, "PUT", "/v1/sites/voltix-id/db/import", "application/json", "{}"); code != 400 {
		t.Errorf("import with JSON type answered %d", code)
	}
	if code, _ := do(t, c, "GET", "/v1/sites/voltix-id/db/backups/..%2fsite.json", ""); code != 400 && code != 404 {
		t.Errorf("backup path trick answered %d", code)
	}
	bl, _ := os.ReadFile(logPath)
	for _, want := range []string{`"action":"file-write"`, `"action":"file-rename"`, `"action":"file-delete"`, `"action":"file-restore"`, `"action":"file-chmod","site":"voltix-id","result":"refused"`} {
		if !strings.Contains(string(bl), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	if strings.Contains(string(bl), "hello2") || strings.Contains(string(bl), "topsecret-xyz") {
		t.Error("audit holds file content or SQL text")
	}
	if !strings.Contains(string(bl), `"action":"db-query","site":"voltix-id","result":"ok","detail":"UPDATE (`) {
		t.Error("audit lacks the query kind")
	}
}
