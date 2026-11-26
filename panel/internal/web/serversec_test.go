package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/authlog"
)

func (f *fakeAgent) serverSec(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	if r.Method == "GET" {
		write(200, f.srv)
		return
	}
	f.srvCalls = append(f.srvCalls, strings.TrimSpace(r.URL.Path+" "+string(b)))
	switch r.URL.Path {
	case "/v1/firewall":
		if strings.Contains(string(b), `"port":"22"`) {
			write(400, map[string]string{"error": "port 22 is SSH; it cannot be closed from the panel"})
			return
		}
		write(200, agent.FWTrial{Started: 1, Deadline: 2, What: "allow 8443 tcp"})
	case "/v1/fail2ban/allow":
		write(200, map[string][]string{"allow": {"203.0.113.9"}})
	default:
		write(200, map[string]bool{"ok": true})
	}
}

func TestServerSecPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/settings/server"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	f.srv = agent.ServerSecurity{Active: true, SSHPorts: []int{20022}, Rules: []agent.FWRule{
		{N: 1, To: "20022/tcp", Action: "ALLOW", From: "Anywhere", Locked: "ssh"},
		{N: 2, To: "8080/tcp", Action: "ALLOW", From: "Anywhere", Comment: "nostek panel"},
		{N: 3, To: "9000:9100/udp", Action: "DENY", From: "198.51.100.0/24"},
	}, Jails: []agent.Jail{{Name: "sshd", Banned: []string{"192.0.2.7"}, Failed: 2, Total: 7}, {Name: "nostek-panel", Banned: []string{}}, {Name: "recidive", Banned: []string{}}},
		Allow: []string{"203.0.113.9"}}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?done=trial", "?done=confirmed", "?done=undone", "?done=unbanned", "?done=allow", "?err=x"} {
			code, body, _ := e.get(e.c, "/settings/server"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/server"`) || missingKey.MatchString(body) {
			t.Errorf("settings (%s) lacks the server security card", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/server")
	for _, want := range []string{`name="match" value="8080/tcp|ALLOW|Anywhere"`, ">SSH</span>", "192.0.2.7", "Panel sign-in", "recidive", "203.0.113.9", "Add rule (on trial)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="match" value="20022/tcp|ALLOW|Anywhere"`) {
		t.Error("the SSH rule can be removed")
	}
	post := func(form url.Values) string {
		_, _, h := e.post(e.c, "/settings/server", form, "/settings/server")
		return h.Get("Location")
	}
	if loc := post(url.Values{"op": {"allow"}, "port": {"8443"}, "proto": {"tcp"}, "password": {"nope"}}); !strings.Contains(loc, "err=") || len(f.srvCalls) != 0 {
		t.Fatalf("wrong password: %s %v", loc, f.srvCalls)
	}
	if loc := post(url.Values{"op": {"allow"}, "port": {"8443"}, "proto": {"tcp"}, "from": {" 203.0.113.0/24 "}, "password": {pw}}); loc != "/settings/server?done=trial#trial" {
		t.Fatalf("change: %s", loc)
	}
	if !strings.Contains(f.srvCalls[0], `"op":"allow","port":"8443","proto":"tcp","from":"203.0.113.0/24"`) {
		t.Fatalf("call %v", f.srvCalls)
	}
	if loc := post(url.Values{"op": {"deny"}, "port": {"22"}, "proto": {"tcp"}, "password": {pw}}); !strings.Contains(loc, "SSH") {
		t.Fatalf("agent refusal: %s", loc)
	}
	// a trial started after this session: confirm needs a new session
	f.srv.Trial = &agent.FWTrial{Started: time.Now().Unix() + 3600, Deadline: time.Now().Unix() + 4200, What: "allow 8443 tcp"}
	_, body, _ = e.get(e.c, "/settings/server")
	if !strings.Contains(body, "new private window") || !strings.Contains(body, `disabled>Keep the change`) || strings.Contains(body, "Add rule (on trial)") {
		t.Error("trial view wrong")
	}
	n := len(f.srvCalls)
	if loc := post(url.Values{"op": {"confirm"}}); !strings.Contains(loc, "err=") || len(f.srvCalls) != n {
		t.Fatalf("confirm from the old session: %s", loc)
	}
	f.srv.Trial.Started = 1
	_, body, _ = e.get(e.c, "/settings/server")
	if strings.Contains(body, `disabled>Keep the change`) {
		t.Error("confirm stays disabled in a newer session")
	}
	if loc := post(url.Values{"op": {"confirm"}}); loc != "/settings/server?done=confirmed#firewall" || f.srvCalls[len(f.srvCalls)-1] != "/v1/firewall/confirm" {
		t.Fatalf("confirm: %s %v", loc, f.srvCalls)
	}
	if loc := post(url.Values{"op": {"undo"}}); loc != "/settings/server?done=undone#firewall" {
		t.Fatalf("undo: %s", loc)
	}
	if loc := post(url.Values{"op": {"unban"}, "jail": {"sshd"}, "ip": {"192.0.2.7"}}); loc != "/settings/server?done=unbanned#bans" || !strings.Contains(f.srvCalls[len(f.srvCalls)-1], `"ip":"192.0.2.7"`) {
		t.Fatalf("unban: %s", loc)
	}
	if loc := post(url.Values{"op": {"allowlist"}, "allow": {"203.0.113.9\r\n198.51.100.0/24, 192.0.2.1"}, "password": {pw}}); loc != "/settings/server?done=allow#allow" ||
		!strings.Contains(f.srvCalls[len(f.srvCalls)-1], `["203.0.113.9","198.51.100.0/24","192.0.2.1"]`) {
		t.Fatalf("allow: %s %v", loc, f.srvCalls)
	}
	audit := e.auditText()
	for _, want := range []string{"firewall.change trial: allow 8443 tcp", "firewall.confirm", "firewall.undo", "fail2ban.unban sshd 192.0.2.7", "fail2ban.allow"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}

// Failed sign-ins land in the fail2ban log; a good one does not.
func TestAuthFailLog(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	p := filepath.Join(t.TempDir(), "auth-fail.log")
	e.srv.authFail = authlog.Open(p, "panel")
	c := e.client()
	e.post(c, "/login", url.Values{"email": {"admin@example.test"}, "password": {"wrong"}}, "/login")
	e.post(c, "/login", url.Values{"email": {"nobody@example.test"}, "password": {"wrong"}}, "/login")
	b, _ := os.ReadFile(p)
	if strings.Count(string(b), " nostek-auth fail ip=") != 2 || !strings.Contains(string(b), "service=panel") {
		t.Fatalf("log:\n%s", b)
	}
}
