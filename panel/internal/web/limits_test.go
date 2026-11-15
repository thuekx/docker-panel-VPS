package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

func TestLimitsCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, page, _ := e.get(e.c, "/sites/voltix-id")
		if code != 200 || !strings.Contains(page, `id="limits"`) || !strings.Contains(page, `name="ram.db"`) || !strings.Contains(page, `<option value="1024" selected>1 GB</option>`) ||
			!strings.Contains(page, `name="php.upload_mb"`) || !strings.Contains(page, `<option value="512" selected>512 MB</option>`) || strings.Contains(page, `<option value="256">256 MB</option></select></td>`) ||
			!strings.Contains(page, "1664 MB") || !strings.Contains(page, "1 GB.") || missingKey.MatchString(page) {
			t.Fatalf("card (%s) %d %v", lang, code, missingKey.FindString(page))
		}
		if !strings.Contains(page, "s-warn") {
			t.Errorf("limits above the server memory not flagged (%s)", lang)
		}
	}
	_, page, _ := e.get(e.c, "/sites/voltix-id")
	form := url.Values{"action": {"limits"}, "ram.db": {"1024"}, "ram.php": {"512"}, "ram.web": {"128"},
		"php.memory_mb": {"512"}, "php.upload_mb": {"512"}, "php.exec_seconds": {"300"}, "php.max_input_vars": {"3000"}}
	code, _, hdr := e.postWith(e.c, "/sites/voltix-id", form, page)
	if code != 303 || !strings.HasSuffix(hdr.Get("Location"), "?done=limits#limits") || len(f.limCalls) != 1 ||
		f.limCalls[0] != `{"ram":{"db":1024,"php":512,"web":128},"php":{"memory_mb":512,"upload_mb":512,"exec_seconds":300,"max_input_vars":3000}}` {
		t.Fatalf("save %d %s %v", code, hdr.Get("Location"), f.limCalls)
	}
	form.Set("ram.db", "256")
	code, _, hdr = e.postWith(e.c, "/sites/voltix-id", form, page)
	if code != 303 || !strings.Contains(hdr.Get("Location"), "err=") || !strings.HasSuffix(hdr.Get("Location"), "#limits") {
		t.Errorf("refused %d %s", code, hdr.Get("Location"))
	}
	form.Set("ram.db", "lots")
	if code, _, hdr = e.postWith(e.c, "/sites/voltix-id", form, page); code != 303 || !strings.Contains(hdr.Get("Location"), "err=") || len(f.limCalls) != 2 {
		t.Errorf("not a number reached the agent %d %v", code, f.limCalls)
	}
}

func TestMB(t *testing.T) {
	for n, want := range map[int]string{128: "128 MB", 768: "768 MB", 1024: "1 GB", 1536: "1.5 GB", 2048: "2 GB", 1664: "1664 MB"} {
		if got := mb(n); got != want {
			t.Errorf("mb(%d) = %q, want %q", n, got, want)
		}
	}
	v := limitsOf(agent.Limits{Services: []agent.ServiceRAM{{Service: "cli", Standard: 512}, {Service: "db", Standard: 512, Current: 512}}, RAMChoice: []int{768, 1024}})
	if len(v.RAM) != 1 || len(v.RAM[0].Choices) != 3 || v.PHP != nil || v.Over {
		t.Errorf("view %+v", v)
	}
}
