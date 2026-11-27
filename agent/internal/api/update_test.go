package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUpdateRoutes(t *testing.T) {
	c, logPath, m := serveM(t, map[int]bool{os.Getuid(): true})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"tag_name":"v1.38.0","name":"v1.38.0","body":"notes"}]`)
	}))
	defer gh.Close()
	m.UseUpdateTest(gh.URL, "file:///repo", t.TempDir(), nil)
	code, out := do(t, c, "GET", "/v1/update", "")
	rel, _ := out["releases"].([]any)
	if code != 200 || len(rel) != 1 || !strings.Contains(out["ready"].(string), "not fully installed") {
		t.Fatalf("view %d %v", code, out)
	}
	for _, body := range []string{`{"tag":"v1.38.0","x":1}`, `{"tag":"` + strings.Repeat("v", 41) + `"}`, `{"tag":"v1.38.0"}`, `{"tag":"main"}`} {
		if code, out := do(t, c, "POST", "/v1/update", body); code != 400 {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), `"update"`) && strings.Contains(string(b), `"ok"`) && strings.Contains(string(b), "job ") {
		t.Fatalf("a refused update was logged as started: %s", b)
	}
}
