package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func (f *fakeAgent) redisAPI(r *http.Request, write func(int, any)) {
	if f.redis == nil {
		write(404, map[string]string{"error": "not found"})
		return
	}
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.redisCalls = append(f.redisCalls, r.URL.Path+" "+strings.TrimSpace(string(b)))
		if strings.HasSuffix(r.URL.Path, "/flush") {
			write(200, map[string]bool{"ok": true})
			return
		}
		write(202, map[string]string{"job": "jfeed000000000002"})
		return
	}
	write(200, f.redis)
}

func TestRedisCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.perf = agent.Perf{CacheDays: []int{1, 7}}
	// an agent without Redis routes: no card, the page still works
	if code, body, _ := e.get(e.c, "/perf/voltix-id"); code != 200 || strings.Contains(body, `id="redis"`) {
		t.Fatalf("old agent: %d", code)
	}
	f.redis = &agent.RedisView{Possible: true, Sizes: []int{64, 128, 256, 512}, Kind: "wordpress"}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/perf/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ := e.get(e.c, "/perf/voltix-id")
	for _, want := range []string{`id="redis"`, "Turn Redis on", `<option value="128" selected>128 MB</option>`, "Redis Object Cache plugin"} {
		if !strings.Contains(body, want) {
			t.Errorf("off card lacks %q", want)
		}
	}
	if strings.Contains(body, "Turn Redis off") {
		t.Error("off card offers turning off")
	}
	// on: a job the page follows
	code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"redis-on"}, "mode": {"cache"}, "mb": {"256"}}, "/perf/voltix-id")
	if code != 303 || !strings.HasSuffix(h.Get("Location"), "?job=jfeed000000000002#job") || f.redisCalls[0] != `/v1/sites/voltix-id/redis {"mb":256,"mode":"cache","on":true}` {
		t.Fatalf("on: %d %v %v", code, h.Get("Location"), f.redisCalls)
	}
	f.perfJob = &agent.Job{ID: "jfeed000000000002", Kind: "redis", Site: "voltix-id", State: "running", Steps: []string{"files", "start redis"}}
	_, body, _ = e.get(e.c, "/perf/voltix-id?job=jfeed000000000002")
	if !strings.Contains(body, "Restarting the website with Redis…") || !strings.Contains(body, "Changing the Redis of the website.") {
		t.Errorf("running job:\n%s", body)
	}
	// bad input never reaches the agent
	n := len(f.redisCalls)
	e.post(e.c, "/perf/voltix-id", url.Values{"op": {"redis-on"}, "mode": {"lru"}, "mb": {"256"}}, "/perf/voltix-id")
	e.post(e.c, "/perf/voltix-id", url.Values{"op": {"redis-on"}, "mode": {"store"}, "mb": {"x"}}, "/perf/voltix-id")
	if len(f.redisCalls) != n {
		t.Errorf("bad input sent: %v", f.redisCalls[n:])
	}
	// on: figures, empty and off
	f.perfJob = nil
	f.redis = &agent.RedisView{Possible: true, Sizes: []int{64, 128, 256, 512}, Kind: "laravel", Running: true,
		Conf: &agent.RedisConf{Mode: "store", MB: 256}, Used: 3 << 20, Keys: 42, Hits: 90, Misses: 10}
	_, body, _ = e.get(e.c, "/perf/voltix-id")
	for _, want := range []string{"Store (kept on disk)</b>", "3 / 256 MB", "<b>42</b>", "90%", `<option value="256" selected>`, `<option value="store" selected>`, "Turn Redis off", "predis/predis"} {
		if !strings.Contains(body, want) {
			t.Errorf("on card lacks %q", want)
		}
	}
	if code, _, h := e.post(e.c, "/perf/voltix-id", url.Values{"op": {"redis-flush"}}, "/perf/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "done=flush") {
		t.Errorf("flush: %d %v", code, h.Get("Location"))
	}
	e.post(e.c, "/perf/voltix-id", url.Values{"op": {"redis-off"}}, "/perf/voltix-id")
	if last := f.redisCalls[len(f.redisCalls)-1]; last != `/v1/sites/voltix-id/redis {"on":false}` {
		t.Errorf("off sent %s", last)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id cache 256 MB") || !strings.Contains(audit, "site.redis-flush") {
		t.Error("audit of the Redis changes")
	}
	// a website that cannot have one
	f.redis = &agent.RedisView{Possible: false, Sizes: []int{64}}
	if _, body, _ = e.get(e.c, "/perf/voltix-id"); !strings.Contains(body, "cannot have a Redis") || strings.Contains(body, `value="redis-on"`) {
		t.Errorf("not possible:\n%s", body)
	}
}
