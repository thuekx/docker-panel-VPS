package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) ionAPI(r *http.Request, write func(int, any)) {
	if f.ion == nil {
		write(404, map[string]string{"error": "not found"})
		return
	}
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.ionCalls = append(f.ionCalls, strings.TrimSpace(string(b)))
		write(202, map[string]string{"job": "jfeed000000000003"})
		return
	}
	write(200, f.ion)
}

func TestIonCubeCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.perf = agent.Perf{CacheDays: []int{1, 7}}
	if _, body, _ := e.get(e.c, "/perf/voltix-id"); strings.Contains(body, `id="ioncube"`) {
		t.Fatal("card from an agent without the routes")
	}
	f.ion = &agent.IonCubeView{Possible: true, PHP: "8.4"}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		if code, body, _ := e.get(e.c, "/perf/voltix-id"); code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ := e.get(e.c, "/perf/voltix-id")
	if !strings.Contains(body, `id="ioncube"`) || !strings.Contains(body, `value="ioncube-on"`) || !strings.Contains(body, "Turn ionCube on") {
		t.Fatalf("off card:\n%s", body)
	}
	code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"ioncube-on"}}, "/perf/voltix-id")
	if code != 303 || !strings.HasSuffix(h.Get("Location"), "?job=jfeed000000000003#job") || f.ionCalls[0] != `{"on":true}` {
		t.Fatalf("on: %d %v %v", code, h.Get("Location"), f.ionCalls)
	}
	f.perfJob = &agent.Job{ID: "jfeed000000000003", Kind: "ioncube", Site: "voltix-id", State: "running", Steps: []string{"loader", "restart website"}}
	if _, body, _ = e.get(e.c, "/perf/voltix-id?job=jfeed000000000003"); !strings.Contains(body, "Restarting the website…") || !strings.Contains(body, "Loader ready") {
		t.Errorf("job page:\n%s", body)
	}
	f.perfJob = nil
	f.ion = &agent.IonCubeView{Possible: true, On: true, PHP: "8.4", Loaded: "the ionCube PHP Loader v14.4.0, Copyright (c) 2002-2025, by ionCube Ltd.",
		Cache: &agent.IonCubeCache{Fetched: 1795700000, SHA256: "ab12", Versions: []string{"8.1", "8.2", "8.3", "8.4"}}}
	_, body, _ = e.get(e.c, "/perf/voltix-id")
	for _, want := range []string{"Loaded</span>", "ionCube PHP Loader v14.4.0", "8.1, 8.2, 8.3, 8.4", "<code class=\"wrap\">ab12</code>", `value="ioncube-off"`} {
		if !strings.Contains(body, want) {
			t.Errorf("on card lacks %q", want)
		}
	}
	e.post(e.c, "/perf/voltix-id", url.Values{"op": {"ioncube-off"}}, "/perf/voltix-id")
	if f.ionCalls[1] != `{"on":false}` {
		t.Errorf("off sent %v", f.ionCalls)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "site.ioncube") || !strings.Contains(audit, "voltix-id off") {
		t.Error("audit of the ionCube changes")
	}
	f.ion = &agent.IonCubeView{Possible: false, Why: "native"}
	if _, body, _ = e.get(e.c, "/perf/voltix-id"); !strings.Contains(body, "shares PHP between websites") || strings.Contains(body, `value="ioncube-on"`) {
		t.Errorf("not possible:\n%s", body)
	}
}
