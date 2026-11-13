package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAutoDeployCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.app = "node"
	const sec = "5ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2e75ec2"
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/sites/voltix-id")
		if code != 200 || !strings.Contains(page, `id="autodeploy"`) || !strings.Contains(page, `value="webhook-on"`) || strings.Contains(page, sec) || missingKey.MatchString(page) {
			t.Fatalf("card (%s) %d %v", lang, code, missingKey.FindString(page))
		}
	}
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	code, on, hdr := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"webhook-on"}}, page)
	if code != 200 || !strings.Contains(on, sec) || !strings.Contains(on, "/hooks/voltix-id") || !strings.Contains(hdr.Get("Cache-Control"), "no-store") || len(f.hookCalls) != 1 {
		t.Fatalf("turn on %d %v", code, f.hookCalls)
	}
	_, page, _ = e.get(e.c, "/sites/voltix-id")
	if strings.Contains(page, sec) || !strings.Contains(page, `value="webhook-off"`) || !strings.Contains(page, "0123456") {
		t.Error("secret shown again, or no off button / deliveries")
	}
	code, _, hdr = e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"webhook-off"}}, page)
	if code != 303 || !strings.Contains(hdr.Get("Location"), "done=webhook-off") || f.hookCalls[len(f.hookCalls)-1] != "remove" {
		t.Errorf("turn off %d %s", code, hdr.Get("Location"))
	}
	// a website built from its own files has no webhook
	f.app = "go"
	if _, page, _ := e.get(e.c, "/sites/voltix-id"); strings.Contains(page, `id="autodeploy"`) {
		t.Error("card on a files website")
	}
	if code, _, _ := e.postWith(e.c, "/sites/voltix-id", url.Values{"action": {"webhook-on"}}, page); code != 400 {
		t.Errorf("turn on for files website: %d", code)
	}
}

func hookPost(t *testing.T, base, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHookDeliver(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	push := `{"ref":"refs/heads/main"}`
	// no session, no CSRF: the agent checks the signature
	code, body := hookPost(t, e.ts.URL, "/hooks/voltix-id", push, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=abc", "Content-Type": "application/json"})
	if code != 200 || !strings.Contains(body, `"result":"deploy"`) || len(f.hooks) != 1 || string(f.hooks[0].Body) != push || f.hooks[0].Event != "push" || f.hooks[0].Signature != "sha256=abc" {
		t.Fatalf("github %d %s %+v", code, body, f.hooks)
	}
	hookPost(t, e.ts.URL, "/hooks/voltix-id", push, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": "tok"})
	if h := f.hooks[len(f.hooks)-1]; h.Event != "Push Hook" || h.Token != "tok" || h.Signature != "" {
		t.Errorf("gitlab %+v", h)
	}
	if code, _ := hookPost(t, e.ts.URL, "/hooks/voltix-id", push, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=bad"}); code != 401 {
		t.Errorf("bad signature %d", code)
	}
	if code, _ := hookPost(t, e.ts.URL, "/hooks/other-site", push, nil); code != 404 {
		t.Errorf("other site %d", code)
	}
	n := len(f.hooks)
	if code, _ := hookPost(t, e.ts.URL, "/hooks/..%2Fetc", push, nil); code != 404 {
		t.Errorf("bad id %d", code)
	}
	if code, _ := hookPost(t, e.ts.URL, "/hooks/voltix-id", strings.Repeat("x", maxHookBody+1), nil); code != 413 || len(f.hooks) != n {
		t.Errorf("large body %d", code)
	}
	r, _ := http.Get(e.ts.URL + "/hooks/voltix-id")
	if r.StatusCode != 405 {
		t.Errorf("GET answered %d", r.StatusCode)
	}
	for i := 0; i < 70; i++ {
		hookPost(t, e.ts.URL, "/hooks/voltix-id", push, nil)
	}
	if code, _ := hookPost(t, e.ts.URL, "/hooks/voltix-id", push, nil); code != 429 {
		t.Errorf("no rate limit: %d", code)
	}
}
