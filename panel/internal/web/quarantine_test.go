package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/alert"
)

const qid = "q0123456789abcdef"

func (f *fakeAgent) quarantine(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	if r.Method == "GET" {
		write(200, map[string]any{"files": f.qFiles})
		return
	}
	f.qCalls = append(f.qCalls, strings.TrimSpace(r.URL.Path+" "+string(b)))
	q := agent.QFile{ID: qid, Site: "voltix-id", Path: "/wp-content/uploads/shell.php"}
	if strings.HasSuffix(r.URL.Path, "/restore") && len(f.qCalls) > 5 {
		write(400, map[string]string{"error": "a file is at /wp-content/uploads/shell.php again; move it away first"})
		return
	}
	write(200, q)
}

func TestQuarantinePage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.qFiles = []agent.QFile{{ID: qid, Site: "voltix-id", Domain: "voltix.id", Path: "/wp-content/uploads/<b>shell.php", Signature: "Php.Webshell-1",
		Size: 2048, SHA256: strings.Repeat("ab", 32), At: 1764200000, Auto: true}}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?done=restore", "?done=delete", "?err=x"} {
			code, body, _ := e.get(e.c, "/settings/quarantine"+q)
			if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
				t.Errorf("%s (%s): %d %v", q, lang, code, missingKey.FindString(body))
			}
		}
		if _, body, _ := e.get(e.c, "/settings"); !strings.Contains(body, `href="/settings/quarantine"`) {
			t.Errorf("settings (%s) lacks the quarantine card", lang)
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/quarantine")
	for _, want := range []string{"voltix.id", "&lt;b&gt;shell.php", "Php.Webshell-1", "abababababababab…", "by the scheduled scan", `value="` + qid + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	post := func(form url.Values) string {
		_, _, h := e.post(e.c, "/settings/quarantine", form, "/settings/quarantine")
		return h.Get("Location")
	}
	if code, _, _ := e.post(e.c, "/settings/quarantine", url.Values{"op": {"restore"}, "id": {"../x"}}, "/settings/quarantine"); code != 400 {
		t.Fatalf("bad id: %d", code)
	}
	if loc := post(url.Values{"op": {"restore"}, "id": {qid}}); loc != "/settings/quarantine?done=restore" {
		t.Fatalf("restore %s", loc)
	}
	if loc := post(url.Values{"op": {"delete"}, "id": {qid}}); loc != "/settings/quarantine?done=delete" {
		t.Fatalf("delete %s", loc)
	}
	if strings.Join(f.qCalls, ",") != "/v1/quarantine/"+qid+"/restore,/v1/quarantine/"+qid+"/delete" {
		t.Fatalf("calls %v", f.qCalls)
	}

	// the Security page: schedule and move to quarantine
	f.secScan = agent.Scan{MinMB: 1400, Last: &agent.ScanResult{Started: 1700000000, Finished: 1700000130, Files: 10,
		Found: []agent.ScanFound{{Path: "/a.php", Signature: "Php.A"}, {Path: "/b.php", Signature: "Php.B", Quarantined: true}}}}
	_, body, _ = e.get(e.c, "/security/voltix-id")
	for _, want := range []string{`name="op" value="scan-schedule"`, `name="op" value="scan-quarantine"><input type="hidden" name="path" value="/a.php"><input type="hidden" name="signature" value="Php.A">`, "In quarantine"} {
		if !strings.Contains(body, want) {
			t.Errorf("security page lacks %q", want)
		}
	}
	spost := func(form url.Values) string {
		_, _, h := e.post(e.c, "/security/voltix-id", form, "/security/voltix-id")
		return h.Get("Location")
	}
	if loc := spost(url.Values{"op": {"scan-schedule"}, "every": {"weekly"}, "quarantine": {"1"}}); loc != "/security/voltix-id?done=schedule#scan" {
		t.Fatalf("schedule %s", loc)
	}
	if last := f.secCalls[len(f.secCalls)-1]; last != `{"every":"weekly","quarantine":true}` {
		t.Fatalf("schedule call %q", last)
	}
	spost(url.Values{"op": {"scan-schedule"}, "every": {""}, "quarantine": {"1"}})
	if last := f.secCalls[len(f.secCalls)-1]; last != `{"every":"","quarantine":false}` {
		t.Fatalf("off call %q", last)
	}
	if code, _, _ := e.post(e.c, "/security/voltix-id", url.Values{"op": {"scan-schedule"}, "every": {"hourly"}}, "/security/voltix-id"); code != 400 {
		t.Fatalf("bad schedule %d", code)
	}
	if loc := spost(url.Values{"op": {"scan-quarantine"}, "path": {"/a.php"}, "signature": {"Php.A"}}); loc != "/security/voltix-id?done=quarantined#scan" {
		t.Fatalf("quarantine %s", loc)
	}
	if last := f.qCalls[len(f.qCalls)-1]; last != `/v1/sites/voltix-id/quarantine {"path":"/a.php","signature":"Php.A"}` {
		t.Fatalf("quarantine call %q", last)
	}
	audit := e.auditText()
	for _, want := range []string{"malware-schedule voltix-id weekly, quarantine", "malware-schedule voltix-id off", "malware-quarantine voltix-id", "quarantine.restore", "quarantine.delete"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}

func TestMalwareAlert(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.srv.SetAlertKey(alert.NewKey([]byte("0123456789abcdef0123456789abcdef")))
	e.srv.alertSync = true
	got := fakeTelegram(t)
	sealed, _ := e.srv.akey.Seal(testTok)
	e.st.SetSetting(kTgChat, "1")
	e.st.SetSetting(kTgToken, sealed)
	e.st.SetSetting(kKindsOff, "disk,memory,backup")
	e.st.SetSetting(kWatchSeq, "1")
	e.srv.cfg.DefaultLang = "en"
	f.watch = agent.WatchView{Seq: 3, Events: []agent.WatchEvent{{Seq: 2, Site: "voltix-id", Service: "malware", Kind: "found", Reason: "2"},
		{Seq: 3, Site: "shop-id", Service: "malware", Kind: "found", Reason: "1 quarantined"}}}
	e.srv.AlertTick(t.Context())
	if len(*got) != 2 || !strings.Contains((*got)[0], "scan of voltix-id found 2 file(s)") || !strings.Contains((*got)[1], "shop-id found 1 file(s) and moved them to the quarantine") {
		t.Fatalf("%q", *got)
	}
}
