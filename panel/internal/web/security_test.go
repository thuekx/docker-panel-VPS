package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// security answers the security routes of the fake agent.
func (f *fakeAgent) security(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
		if f.secJob == nil {
			write(404, map[string]string{"error": "job not found"})
			return
		}
		write(200, f.secJob)
		return
	}
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/security/scan") {
		f.secCalls = append(f.secCalls, "scan")
		if f.secScan.Job != nil && f.secScan.Job.State == "running" {
			write(409, map[string]string{"error": "busy: another malware scan runs now"})
			return
		}
		write(202, map[string]string{"job": "jsec0000000000002"})
		return
	}
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.secCalls = append(f.secCalls, strings.TrimSpace(string(b)))
		if strings.Contains(string(b), `"ip":"bad"`) {
			write(400, map[string]string{"error": "bad input: \"bad\" is not an IP address or network"})
			return
		}
		if strings.Contains(string(b), `"op":"hotlink-off"`) {
			write(200, map[string]string{"job": ""})
			return
		}
		write(202, map[string]string{"job": "jsec0000000000001"})
		return
	}
	write(200, agent.Security{Blocked: []string{"198.51.100.9", "2001:db8::/32"}, Protected: []agent.Protected{{Path: "/admin", Users: []string{"ana", "budi"}}, {Path: "/solo", Users: []string{"cici"}}},
		Hotlink: &agent.Hotlink{Allow: []string{"google.com"}}, HotlinkExts: []string{"jpg", "png"}, Job: f.secJob,
		WebServer: true, ErrorCodes: []int{403, 404, 503}, ErrorPages: []int{404}, ErrorDir: "/.nostek-errors", MIME: []agent.MIME{{Type: "application/wasm", Exts: []string{"wasm"}}},
		Logins: f.secLogins, Scan: f.secScan, WAF: f.secWAF, WAFReady: f.secWAFOK})
}

func TestSecurityPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/security/voltix-id"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	jobs := []*agent.Job{nil,
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "running", Steps: []string{"routes"}},
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "failed", Steps: []string{"routes"}, Error: "compose up failed"},
		{ID: "jsec0000000000001", Kind: "security", Site: "voltix-id", State: "done", Steps: []string{"routes"}},
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, j := range jobs {
			f.secJob = j
			for _, q := range []string{"", "?job=jsec0000000000001", "?done=nochange"} {
				code, body, _ := e.get(e.c, "/security/voltix-id"+q)
				if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
					t.Errorf("%s (%s, %v): %d or missing key %v", q, lang, j, code, missingKey.FindString(body))
				}
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	f.secJob = nil
	_, body, _ := e.get(e.c, "/security/voltix-id")
	for _, want := range []string{
		`href="/security/voltix-id" aria-current="page"`,
		`name="ip" value="198.51.100.9"`,
		"<code>2001:db8::/32</code>",
		"Files: jpg, png",
		">google.com</textarea>",
		`name="user" value="ana"`,
		"Save allowed sites",
		`href="/files/voltix-id/edit?path=%2f.nostek-errors%2f404.html"`,
		`name="op" value="errpage-on"><input type="hidden" name="code" value="503">`,
		`name="op" value="errpage-off"><input type="hidden" name="code" value="404">`,
		"<code>application/wasm</code> <span class=\"muted\">.wasm</span>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="user" value="cici"><code>cici</code> <button`) {
		t.Error("the last user of a folder can be removed")
	}
	f.secJob = jobs[3]
	if _, body, _ = e.get(e.c, "/security/voltix-id?job=jsec0000000000001"); !strings.Contains(body, "Done. The rule is live.") {
		t.Error("finished job not shown")
	}
	f.secJob = nil
	pw := "correct horse"
	for _, c := range []struct {
		form url.Values
		loc  string
	}{
		{url.Values{"op": {"block-add"}, "ip": {" 203.0.113.7 "}}, "/security/voltix-id?job=jsec0000000000001#job"},
		{url.Values{"op": {"block-add"}, "ip": {""}}, "err="},
		{url.Values{"op": {"block-add"}, "ip": {"bad"}}, "err=bad+input"},
		{url.Values{"op": {"block-remove"}, "ip": {"198.51.100.9"}}, "#job"},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {"short"}, "password2": {"short"}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {pw}, "password2": {pw + "x"}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {""}, "user": {"ana"}, "password": {pw}, "password2": {pw}}, "err="},
		{url.Values{"op": {"protect-set"}, "path": {"/admin"}, "user": {"ana"}, "password": {pw}, "password2": {pw}}, "#job"},
		{url.Values{"op": {"protect-user-remove"}, "path": {"/admin"}, "user": {"budi"}}, "#job"},
		{url.Values{"op": {"protect-remove"}, "path": {"/admin"}}, "err="},
		{url.Values{"op": {"protect-remove"}, "path": {"/admin"}, "confirm": {"1"}}, "#job"},
		{url.Values{"op": {"hotlink-on"}, "allow": {"google.com\r\nbing.com, duck.com"}}, "#job"},
		{url.Values{"op": {"hotlink-off"}}, "done=nochange"},
		{url.Values{"op": {"errpage-on"}, "code": {"503"}}, "#job"},
		{url.Values{"op": {"mime-add"}, "type": {"text/x-tex"}, "exts": {""}}, "err="},
		{url.Values{"op": {"mime-add"}, "type": {" text/x-tex "}, "exts": {"tex ltx"}}, "#job"},
		{url.Values{"op": {"mime-remove"}, "type": {"application/wasm"}}, "#job"},
	} {
		if code, _, h := e.post(e.c, "/security/voltix-id", c.form, "/security/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), c.loc) {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
	}
	if code, _, _ := e.post(e.c, "/security/voltix-id", url.Values{"op": {"nosuch"}}, "/security/voltix-id"); code != 400 {
		t.Errorf("odd op: %d", code)
	}
	var hashed agent.SecurityChange
	var rest []string
	for _, c := range f.secCalls {
		if strings.Contains(c, `"op":"protect-set"`) {
			_ = json.Unmarshal([]byte(c), &hashed)
			continue
		}
		rest = append(rest, c)
	}
	want := []string{`{"op":"block-add","ip":"203.0.113.7"}`, `{"op":"block-add","ip":"bad"}`, `{"op":"block-remove","ip":"198.51.100.9"}`,
		`{"op":"protect-user-remove","path":"/admin","user":"budi"}`, `{"op":"protect-remove","path":"/admin"}`,
		`{"op":"hotlink-on","allow":["google.com","bing.com","duck.com"]}`, `{"op":"hotlink-off"}`,
		`{"op":"errpage-on","code":503}`, `{"op":"mime-add","type":"text/x-tex","exts":"tex ltx"}`, `{"op":"mime-remove","type":"application/wasm"}`}
	if strings.Join(rest, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(rest, "\n"))
	}
	// only a bcrypt hash of the password reaches the agent
	if hashed.User != "ana" || bcrypt.CompareHashAndPassword([]byte(hashed.Hash), []byte(pw)) != nil || strings.Contains(strings.Join(f.secCalls, ""), pw) {
		t.Errorf("protect-set %+v", hashed)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id block-add 203.0.113.7") || !strings.Contains(audit, "protect-set /admin user ana") || strings.Contains(audit, "$2a$") {
		t.Error("audit lacks the security changes or holds a hash")
	}
}

func TestSecurityWave3(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/security/voltix-id")
	for _, want := range []string{"Not counting yet", "Not scanned yet.", `name="op" value="scan"`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty page lacks %q", want)
		}
	}
	f.secLogins = agent.Logins{Active: true, Since: 1700000000, Day: 41, DayFail: 40, Week: 50, IPs: []agent.LoginIP{
		{IP: "198.51.100.7", Day: 40, DayFail: 40, Week: 45, LastSeen: 1700003600, LastPath: "/wp-login.php"},
		{IP: "192.0.2.9", Day: 1, Week: 5, LastSeen: 1700000000, LastPath: "/xmlrpc.php", Blocked: true}}}
	f.secScan = agent.Scan{MinMB: 1400, Last: &agent.ScanResult{Started: 1700000000, Finished: 1700000130, Files: 1543,
		Signatures: "3627845 signatures, ClamAV 1.5.5", Found: []agent.ScanFound{
			{Path: "/wp-content/uploads/<b>shell.php", Signature: "Php.Webshell-1"}, {Path: "/old.js", Signature: "Js.Agent-2", Gone: true}}}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, j := range []*agent.Job{nil, {ID: "jsec0000000000002", Kind: "malware-scan", Site: "voltix-id", State: "running", Steps: []string{"signatures", "scan"}},
			{ID: "jsec0000000000002", Kind: "malware-scan", Site: "voltix-id", State: "failed", Steps: []string{"signatures"}, Error: "downloading the signatures failed"}} {
			f.secScan.Job, f.secJob = j, j
			code, body, _ := e.get(e.c, "/security/voltix-id?job=jsec0000000000002")
			if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
				t.Errorf("%s %v: %d %v", lang, j, code, missingKey.FindString(body))
			}
		}
	}
	f.secScan.Job, f.secJob = &agent.Job{ID: "jsec0000000000002", Kind: "malware-scan", Site: "voltix-id", State: "running", Steps: []string{"scan"}}, nil
	_, body, _ = e.get(e.c, "/security/voltix-id")
	for _, want := range []string{
		"Last 24 hours: <b>41</b> (40 failed)",
		`name="op" value="block-add"><input type="hidden" name="ip" value="198.51.100.7">`,
		`<span class="pill s-bad">Blocked</span>`,
		"2 file(s) look like malware",
		"&lt;b&gt;shell.php",
		`href="/files/voltix-id?path=%2fwp-content%2fuploads"`,
		`name="op" value="scan-trash"><input type="hidden" name="path" value="/wp-content/uploads/&lt;b&gt;shell.php">`,
		"Removed",
		"Malware scan</b>", // the running scan shows as its own job
		"Scan running…",
		"Last scan ", "(3 min)", "1543 files",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="op" value="block-add"><input type="hidden" name="ip" value="192.0.2.9">`) {
		t.Error("a blocked address offers Block")
	}
	// a running scan does not lock the other changes
	if strings.Contains(body, `type="submit" data-busy="Working…" disabled>Block`) {
		t.Error("blocking is locked during a scan")
	}
	f.secScan.Job = nil
	for _, c := range []struct {
		form url.Values
		loc  string
	}{
		{url.Values{"op": {"scan"}}, "/security/voltix-id?job=jsec0000000000002#job"},
		{url.Values{"op": {"scan-trash"}, "path": {"/wp-content/uploads/shell.php"}}, "err="},
		{url.Values{"op": {"scan-trash"}, "path": {"/wp-content/uploads/shell.php"}, "confirm": {"1"}}, "done=trashed#scan"},
	} {
		if code, _, h := e.post(e.c, "/security/voltix-id", c.form, "/security/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), c.loc) {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
	}
	if code, _, _ := e.post(e.c, "/security/voltix-id", url.Values{"op": {"scan-trash"}, "path": {"relative"}, "confirm": {"1"}}, "/security/voltix-id"); code != 400 {
		t.Errorf("relative path: %d", code)
	}
	f.secScan.Job = &agent.Job{State: "running"}
	if _, _, h := e.post(e.c, "/security/voltix-id", url.Values{"op": {"scan"}}, "/security/voltix-id"); !strings.Contains(h.Get("Location"), "err=") {
		t.Errorf("second scan: %s", h.Get("Location"))
	}
	if strings.Join(f.ops, ",") != "delete /wp-content/uploads/shell.php" {
		t.Errorf("file ops %v", f.ops)
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "malware-trash") || !strings.Contains(audit, "voltix-id /wp-content/uploads/shell.php") {
		t.Error("audit lacks the scan actions")
	}
}

func TestSecurityWAF(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/security/voltix-id")
	if !strings.Contains(body, `id="waf"`) || !strings.Contains(body, "panel update") {
		t.Errorf("WAF card without the service lacks the update note")
	}
	f.secWAFOK = true
	f.secWAF = agent.WAF{On: true, Mode: "block", Paranoia: 2, Exclude: []int{941100}, Skip: []string{"/wp-admin/"}, Blocked: 3,
		Events: []agent.WAFEvent{{Time: 1700000000, IP: "203.0.113.9", URI: "/?id=<script>", Rule: 949110, Msg: "Inbound Anomaly Score Exceeded", Action: "blocked"},
			{Time: 1700000000, IP: "203.0.113.9", URI: "/?id=1", Rule: 942100, Msg: "SQL Injection Attack Detected via libinjection", Action: "detected"}},
		Rules: []agent.WAFRule{{Rule: 942100, Msg: "SQL Injection Attack Detected via libinjection", Count: 9}, {Rule: 941100, Msg: "XSS", Count: 1, Excluded: true}}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		code, body, _ := e.get(e.c, "/security/voltix-id")
		if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %d %v", lang, code, missingKey.FindString(body))
		}
	}
	_, body, _ = e.get(e.c, "/security/voltix-id")
	for _, want := range []string{
		`<option value="block" selected>`, `<option value="2" selected>`,
		`name="op" value="waf-exclude-add"><input type="hidden" name="rule" value="942100">`,
		`name="op" value="waf-exclude-remove"><input type="hidden" name="rule" value="941100">`,
		`name="op" value="waf-skip-remove"><input type="hidden" name="path" value="/wp-admin/">`,
		"/?id=&lt;script&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "comes with the panel update") {
		t.Error("update note shown although the service runs")
	}
	f.secCalls = nil
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"op": {"waf-set"}, "mode": {"detect"}, "level": {"1"}}, `{"op":"waf-set","mode":"detect","level":1}`},
		{url.Values{"op": {"waf-set"}, "mode": {"off"}, "level": {"1"}}, `{"op":"waf-off"}`},
		{url.Values{"op": {"waf-exclude-add"}, "rule": {" 942100 "}}, `{"op":"waf-exclude-add","rule":942100}`},
		{url.Values{"op": {"waf-exclude-remove"}, "rule": {"941100"}}, `{"op":"waf-exclude-remove","rule":941100}`},
		{url.Values{"op": {"waf-skip-add"}, "path": {"/api/"}}, `{"op":"waf-skip-add","path":"/api/"}`},
		{url.Values{"op": {"waf-skip-remove"}, "path": {"/wp-admin/"}}, `{"op":"waf-skip-remove","path":"/wp-admin/"}`},
	} {
		if code, _, h := e.post(e.c, "/security/voltix-id", c.form, "/security/voltix-id"); code != 303 || strings.Contains(h.Get("Location"), "err=") {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
		if len(f.secCalls) == 0 || f.secCalls[len(f.secCalls)-1] != c.want {
			t.Errorf("%v sent %v", c.form, f.secCalls)
		}
	}
	n := len(f.secCalls)
	for _, bad := range []url.Values{{"op": {"waf-exclude-add"}, "rule": {"abc"}}, {"op": {"waf-skip-add"}, "path": {" "}}} {
		if _, _, h := e.post(e.c, "/security/voltix-id", bad, "/security/voltix-id"); !strings.Contains(h.Get("Location"), "err=") {
			t.Errorf("%v: %s", bad, h.Get("Location"))
		}
	}
	if len(f.secCalls) != n {
		t.Error("bad input reached the agent")
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id waf-off") || !strings.Contains(audit, "942100") {
		t.Error("audit lacks the WAF changes")
	}
}
