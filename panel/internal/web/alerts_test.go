package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/alert"
)

const testTok = "123456789:AAH-test-token-abcdefghijklmnopqrstuv"

// fakeTelegram answers like the Bot API and keeps what it got.
func fakeTelegram(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	got := &[]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/bot"+testTok+"/sendMessage" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		*got = append(*got, r.PostForm.Get("text"))
		mu.Unlock()
	}))
	t.Cleanup(ts.Close)
	old := alert.TelegramAPI
	alert.TelegramAPI = ts.URL
	t.Cleanup(func() { alert.TelegramAPI = old })
	return got
}

func TestAlertSettings(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin(e.c, "admin@example.test")
	// without a key nothing can be set up
	if code, body, _ := e.post(e.c, "/settings/alerts", url.Values{"op": {"telegram"}, "chat": {"1"}, "token": {testTok}}, "/settings"); code != 422 || !strings.Contains(body, "no key file") {
		t.Fatalf("no key: %d", code)
	}
	e.srv.SetAlertKey(alert.NewKey([]byte("0123456789abcdef0123456789abcdef")))
	e.srv.alertSync = true
	got := fakeTelegram(t)
	for _, bad := range []url.Values{{"op": {"telegram"}, "chat": {"1"}, "token": {"nope"}}, {"op": {"telegram"}, "chat": {"x y"}, "token": {testTok}},
		{"op": {"telegram"}, "chat": {"1"}}, {"op": {"mail"}, "host": {"smtp.example.com"}, "port": {"25"}, "from": {"p@example.com"}, "to": {"a@example.com"}}, {"op": {"launch"}}} {
		if code, _, _ := e.post(e.c, "/settings/alerts", bad, "/settings"); code != 422 && code != 400 {
			t.Errorf("%v accepted: %d", bad, code)
		}
	}
	if code, _, h := e.post(e.c, "/settings/alerts", url.Values{"op": {"telegram"}, "chat": {"123456789"}, "token": {testTok}}, "/settings"); code != 303 || !strings.HasSuffix(h.Get("Location"), "#alerts") {
		t.Fatalf("telegram: %d %s", code, h.Get("Location"))
	}
	if code, _, _ := e.post(e.c, "/settings/alerts", url.Values{"op": {"mail"}, "host": {"smtp.example.com"}, "port": {"587"}, "user": {"panel"}, "pass": {"Mail-Pass-0123"}, "from": {"panel@example.com"}, "to": {"a@example.com, b@example.com"}}, "/settings"); code != 303 {
		t.Fatalf("mail: %d", code)
	}
	// the secrets are stored sealed, never shown, never in the audit log
	for _, k := range []string{kTgToken, kMailPass} {
		if v := e.st.Setting(k, ""); v == "" || strings.Contains(v, "AAH-test") || strings.Contains(v, "Mail-Pass") {
			t.Errorf("%s stored as %q", k, v)
		}
	}
	_, body, _ := e.get(e.c, "/settings")
	_, audit, _ := e.get(e.c, "/audit")
	for _, secret := range []string{testTok, "AAH-test", "Mail-Pass-0123"} {
		if strings.Contains(body, secret) || strings.Contains(audit, secret) {
			t.Errorf("%q shown", secret)
		}
	}
	if !strings.Contains(body, `value="123456789"`) || !strings.Contains(body, "a@example.com, b@example.com") || !strings.Contains(audit, "alerts.telegram") {
		t.Error("settings not shown")
	}
	// saving the chat again with an empty token keeps the token
	if code, _, _ := e.post(e.c, "/settings/alerts", url.Values{"op": {"telegram"}, "chat": {"-1001234567890"}}, "/settings"); code != 303 {
		t.Fatalf("keep token: %d", code)
	}
	if tg, ok := e.srv.telegram(); !ok || tg.Token != testTok || tg.Chat != "-1001234567890" {
		t.Errorf("%+v", tg)
	}
	// test message: Telegram gets it; the e-mail server does not exist, which the list says without the password
	e.srv.st.SetSetting(kMailHost, "smtp.invalid.test")
	if code, _, h := e.post(e.c, "/settings/alerts", url.Values{"op": {"test"}}, "/settings"); code != 303 || !strings.Contains(h.Get("Location"), "saved=test") {
		t.Fatalf("test: %d", code)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "Nostek Panel · test-host") || !strings.Contains((*got)[0], "admin@example.test") {
		t.Fatalf("telegram got %q", *got)
	}
	_, body, _ = e.get(e.c, "/settings")
	if !strings.Contains(body, "Telegram: OK") || !strings.Contains(body, "e-mail: cannot connect") || strings.Contains(body, "Mail-Pass") {
		t.Error("sent list")
	}
	// kinds: switch login off
	e.post(e.c, "/settings/alerts", url.Values{"op": {"kinds"}, "k_watchdog": {"1"}, "k_disk": {"1"}, "k_memory": {"1"}, "k_backup": {"1"}}, "/settings")
	if e.srv.kindOn("login") || !e.srv.kindOn("disk") {
		t.Error("kinds")
	}
	// turning off removes the stored secret
	e.post(e.c, "/settings/alerts", url.Values{"op": {"telegram-off"}}, "/settings")
	if e.st.Setting(kTgToken, "x") != "" {
		t.Error("token kept after off")
	}
	for _, lang := range []string{"id", "en"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		_, body, _ := e.get(e.c, "/settings")
		if missingKey.MatchString(body) || strings.Contains(body, "%!") {
			t.Errorf("%s: %v", lang, missingKey.FindString(body))
		}
	}
}

func TestAlertTickAndLoginAlert(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.srv.SetAlertKey(alert.NewKey([]byte("0123456789abcdef0123456789abcdef")))
	e.srv.alertSync = true
	got := fakeTelegram(t)
	sealed, _ := e.srv.akey.Seal(testTok)
	e.st.SetSetting(kTgChat, "1")
	e.st.SetSetting(kTgToken, sealed)
	e.st.SetSetting(kKindsOff, "disk") // the real disk of the test machine is unknown
	// watchdog: the first look only takes the position, later events are sent
	f.watch = agent.WatchView{Seq: 4, Events: []agent.WatchEvent{{Seq: 4, Site: "voltix-id", Service: "web", Kind: "restarted", Reason: "unhealthy"}}}
	e.srv.AlertTick(t.Context())
	if len(*got) != 0 || e.st.Setting(kWatchSeq, "") != "4" {
		t.Fatalf("old news sent: %q", *got)
	}
	f.watch = agent.WatchView{Seq: 6, Events: []agent.WatchEvent{{Seq: 5, Service: "panel", Kind: "gave_up", Reason: "not running"}, {Seq: 6, Site: "voltix-id", Service: "web", Kind: "recovered"}}}
	e.srv.AlertTick(t.Context())
	if len(*got) != 2 || !strings.Contains((*got)[0], "panel is still not running after 3 restarts") || !strings.Contains((*got)[1], "voltix-id (web) runs well again") {
		t.Fatalf("%q", *got)
	}
	if f.watchAsk[len(f.watchAsk)-1] != "since=4" {
		t.Errorf("asked %v", f.watchAsk)
	}
	// memory at the red level: one alert after 5 minutes, one when it is back
	proc := e.srv.sys.ProcDir
	os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n"), 0o644)
	*got = nil
	f.watch = agent.WatchView{Seq: 6}
	for i := 0; i < 8; i++ {
		e.srv.AlertTick(t.Context())
	}
	os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal: 4000000 kB\nMemAvailable: 3000000 kB\n"), 0o644)
	e.srv.AlertTick(t.Context())
	e.srv.AlertTick(t.Context())
	if len(*got) != 2 || !strings.Contains((*got)[0], "Memory almost full") || !strings.Contains((*got)[1], "back to normal") {
		t.Fatalf("memory %q", *got)
	}
	// nightly backup failed: once
	os.WriteFile(e.srv.sys.BackupFile, []byte("result=FAIL unix="+strconv.FormatInt(time.Now().Unix(), 10)+"\n"), 0o644)
	*got = nil
	e.srv.AlertTick(t.Context())
	e.srv.AlertTick(t.Context())
	if len(*got) != 1 || !strings.Contains((*got)[0], "backup failed") {
		t.Fatalf("backup %q", *got)
	}
	// sign-ins: the second address alerts and shows on the Profile page
	u, _ := e.st.UserByEmail("admin@example.test")
	*got = nil
	sign := func(ip, res string) {
		r := httptest.NewRequest("POST", "/login/2fa", nil)
		r.RemoteAddr = ip + ":5555"
		r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Chrome/140.0 Safari/537.36")
		e.srv.recordLogin(u, r, res)
	}
	sign("198.51.100.1", "ok")
	sign("203.0.113.7", "password")
	sign("203.0.113.7", "ok")
	if len(*got) != 1 || !strings.Contains((*got)[0], "admin@example.test from address 203.0.113.7 (Chrome, Linux)") {
		t.Fatalf("login %q", *got)
	}
	_, body, _ := e.get(e.c, "/profile")
	for _, want := range []string{"Login history", "<code>203.0.113.7</code>", "New address", "Wrong password", "Chrome, Linux"} {
		if !strings.Contains(body, want) {
			t.Errorf("profile lacks %q", want)
		}
	}
	// switched off: nothing is sent
	e.st.SetSetting(kKindsOff, "login,disk")
	sign("192.0.2.50", "ok")
	if len(*got) != 1 {
		t.Error("login alert sent while off")
	}
}

func TestUsageShown(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	now := time.Now().Truncate(time.Hour)
	f.usage = agent.Usage{CPUs: 2, MemMax: 1 << 30, Hours: []agent.UsagePoint{
		{At: now.Add(-2 * time.Hour).Unix(), CPU: 50, Mem: 256 << 20, Disk: 40, Load: 0.5},
		{At: now.Add(-time.Hour).Unix(), CPU: 150, Mem: 512 << 20, Disk: 41, Load: 1.25},
		{At: now.Add(-40 * 24 * time.Hour).Unix(), CPU: 1, Mem: 1},
	}}
	_, body, _ := e.get(e.c, "/metrics/voltix-id?days=1")
	for _, want := range []string{`id="usage"`, "150.0%", `data-bar="75"`, "512.0 MB", `data-bar="50"`, "one of the server&#39;s 2 CPUs", "Memory limit: 1.0 GB"} {
		if !strings.Contains(body, want) {
			t.Errorf("statistics lack %q", want)
		}
	}
	if i, j := strings.Index(body, "150.0%"), strings.Index(body, "50.0%"); i < 0 || j < i {
		t.Error("hours not newest first")
	}
	_, body, _ = e.get(e.c, "/")
	for _, want := range []string{"Server use, last 24 hours", "150.0%", "41%", "1.25"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	f.usage = agent.Usage{}
	_, body, _ = e.get(e.c, "/")
	if !strings.Contains(body, "No samples yet") {
		t.Error("empty usage")
	}
}
