package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) perfAPI(r *http.Request, write func(int, any)) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/jobs/"):
		if f.perfJob == nil {
			write(404, map[string]string{"error": "job not found"})
			return
		}
		write(200, f.perfJob)
	case r.Method == "POST":
		b, _ := io.ReadAll(r.Body)
		f.perfCalls = append(f.perfCalls, strings.TrimSpace(string(b)))
		if strings.Contains(string(b), `"php":"8.2"`) {
			write(400, map[string]string{"error": "bad input: Laravel 13 needs PHP 8.3 or newer"})
			return
		}
		write(202, map[string]string{"job": "jfeed000000000001"})
	default:
		write(200, f.perf)
	}
}

func TestPerfPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.perf = agent.Perf{CacheDays: []int{1, 7, 30, 365}, PHP: &agent.PHPState{Current: "8.4", Previous: "8.3", Choices: []agent.PHPChoice{
		{Version: "8.1", EOL: true}, {Version: "8.2", Why: "Laravel 13 needs PHP 8.3 or newer"}, {Version: "8.3"}, {Version: "8.4"}, {Version: "8.5"}}}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/perf/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ := e.get(e.c, "/perf/voltix-id")
	for _, want := range []string{`href="/perf/voltix-id" aria-current="page"`, `<option value="8.4" selected disabled>PHP 8.4 · now</option>`,
		`<option value="8.2" disabled>PHP 8.2 · Laravel 13 needs PHP 8.3 or newer</option>`, "PHP 8.1 · no more security fixes",
		`name="php" value="8.3"`, "Back to PHP 8.3", `<option value="0" selected>`, `name="compress" value="1">`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if _, site, _ := e.get(e.c, "/sites/voltix-id"); !strings.Contains(site, `href="/perf/voltix-id#php"`) {
		t.Error("overview has no link to change PHP")
	}
	// a change starts the job; the page follows it
	code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"php"}, "php": {"8.5"}}, "/perf/voltix-id")
	if code != 303 || !strings.HasSuffix(h.Get("Location"), "?job=jfeed000000000001#job") || f.perfCalls[0] != `{"php":"8.5"}` {
		t.Fatalf("%d %v %v", code, h.Get("Location"), f.perfCalls)
	}
	f.perfJob = &agent.Job{ID: "jfeed000000000001", Kind: "php", Site: "voltix-id", State: "done",
		Steps: []string{"pull new images", "copy database", "copy files", "recreate containers", "done before-php-20261121-230000.tar.gz"}}
	_, body, _ = e.get(e.c, "/perf/voltix-id?job=jfeed000000000001")
	for _, want := range []string{"Done. The website runs on the new PHP version.", "before-php-20261121-230000.tar.gz", "Website on the new PHP"} {
		if !strings.Contains(body, want) {
			t.Errorf("job lacks %q", want)
		}
	}
	f.perfJob = &agent.Job{ID: "jfeed000000000001", Kind: "php", Site: "voltix-id", State: "running", Steps: []string{"pull new images", "copy database"}}
	_, body, _ = e.get(e.c, "/perf/voltix-id?job=jfeed000000000001")
	if !strings.Contains(body, "Backing up the database…") || !strings.Contains(body, `data-step="recreate containers"`) || !strings.Contains(body, `type="submit" data-confirm="Change the PHP version of this website? A backup is made first." disabled>`) {
		t.Errorf("running job page:\n%s", body)
	}
	// refused below the minimum; wrong input never reaches the agent
	n := len(f.perfCalls)
	if code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"php"}, "php": {"8.2"}}, "/perf/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "err=") {
		t.Errorf("refused: %d %v", code, h.Get("Location"))
	}
	if code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"php"}, "php": {"8.4; rm"}}, "/perf/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "err=") || len(f.perfCalls) != n+1 {
		t.Errorf("bad version: %d %v", code, h.Get("Location"))
	}
	// Optimize Website goes as a security change
	f.secJob = nil
	if code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"optimize"}, "compress": {"1"}, "days": {"7"}}, "/perf/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "?job=") {
		t.Errorf("optimize: %d %v", code, h.Get("Location"))
	}
	if last := f.secCalls[len(f.secCalls)-1]; last != `{"op":"optimize-set","compress":true,"days":7}` {
		t.Errorf("sent %s", last)
	}
	// a website without PHP
	f.perf = agent.Perf{CacheDays: []int{1, 7}, Optimize: agent.Optimize{Compress: true, CacheDays: 7}}
	_, body, _ = e.get(e.c, "/perf/voltix-id")
	if !strings.Contains(body, "runs no PHP") || !strings.Contains(body, `<option value="7" selected>7 day(s)</option>`) || !strings.Contains(body, `value="1" checked>`) {
		t.Errorf("no-PHP page:\n%s", body)
	}
}
