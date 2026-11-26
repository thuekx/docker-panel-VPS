package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/alert"
)

func (f *fakeAgent) backupPlan(r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	p := r.URL.Path
	if r.Method == "POST" {
		f.planCalls = append(f.planCalls, strings.TrimSpace(p+" "+string(b)))
	}
	switch {
	case p == "/v1/backup-plan" && r.Method == "GET":
		write(200, f.plan)
	case p == "/v1/backup-plan/run":
		f.sbkSeen, _ = os.ReadFile(f.sbkCopy)
		f.plan.Due = false
		write(202, map[string]string{"job": "j5bc0000000000002"})
	case p == "/v1/backup-plan/dests":
		if strings.Contains(string(b), `"bucket":"Bad"`) {
			write(400, map[string]string{"error": "the bucket name is not valid"})
			return
		}
		write(200, map[string]string{"id": "d0000aaaa"})
	case strings.HasSuffix(p, "/test"):
		if strings.Contains(p, "d0000bbbb") {
			write(200, agent.DestResult{At: 1764151200, OK: false, Msg: "sftp: Permission denied (publickey)"})
			return
		}
		write(200, agent.DestResult{At: 1764151200, OK: true})
	case p == "/v1/backup-plan/ssh-key":
		write(200, map[string]string{"key": "ssh-ed25519 AAAA nostek-backup@test"})
	case strings.HasSuffix(p, "/send"):
		write(202, map[string]string{"job": "j5bc0000000000003"})
	default:
		write(200, map[string]bool{"ok": true})
	}
}

func TestBackupPlanPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	f.sbkCopy = e.srv.panelCopy()
	e.setupAdmin(e.c, "admin@example.test")
	f.plan = agent.BackupPlan{Every: "weekly", Hour: 2, Weekday: 0, Sealed: true, LastRun: 1764151200, LastOK: false, LastMsg: "copy failed: Box",
		Next: 1764756000, LastFile: sbkName, SSHKey: "ssh-ed25519 AAAA nostek-backup@test", Dests: []agent.BackupDest{
			{ID: "d0000aaaa", Kind: "s3", Name: "IDCloudHost", On: true, Keep: 7, Endpoint: "https://is3.cloudhost.id", Bucket: "nostek-bk", Prefix: "srv", AccessKey: "AKTEST", HasSecret: true,
				Last: &agent.DestResult{At: 1764151200, OK: true, File: sbkName}, Test: &agent.DestResult{At: 1764151200, OK: true}},
			{ID: "d0000bbbb", Kind: "sftp", Name: "Box", On: true, Keep: 3, Host: "backup.example.net", Port: 2222, User: "nostek", Path: "backups", HostKey: "SHA256:abc",
				Last: &agent.DestResult{At: 1764151200, OK: false, Msg: "sftp: Permission denied"}},
		}}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?add=s3", "?add=sftp", "?dest=d0000aaaa", "?dest=d0000bbbb", "?done=schedule", "?done=dest", "?done=destdelete", "?done=desttest", "?done=sshkey"} {
			code, body, _ := e.get(e.c, "/settings/backup"+q)
			if code != 200 || missingKey.MatchString(body) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/settings/backup?dest=d0000aaaa")
	for _, want := range []string{"Once a week", "https://is3.cloudhost.id/nostek-bk/srv", "nostek@backup.example.net:2222/backups", "SHA256:abc",
		"sftp: Permission denied", `name="access_key" required value="AKTEST"`, "empty = keep the stored key", "Copy to IDCloudHost", `value="weekly" selected`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	_, body, _ = e.get(e.c, "/settings/backup?add=sftp")
	if !strings.Contains(body, "ssh-ed25519 AAAA nostek-backup@test") {
		t.Error("the SSH key is not shown")
	}
	post := func(v url.Values) string {
		_, _, h := e.post(e.c, "/settings/backup", v, "/settings/backup")
		return h.Get("Location")
	}
	const pass = "a long schedule passphrase"
	if loc := post(url.Values{"op": {"schedule"}, "every": {"daily"}, "hour": {"3"}, "weekday": {"0"}, "password": {"nope"}}); !strings.Contains(loc, "err=") {
		t.Fatalf("wrong password %s", loc)
	}
	if loc := post(url.Values{"op": {"schedule"}, "every": {"daily"}, "hour": {"3"}, "weekday": {"0"}, "password": {pw}, "passphrase": {pass}, "passphrase2": {"other one!!!!"}}); !strings.Contains(loc, "err=") {
		t.Fatalf("mismatch %s", loc)
	}
	if code, _, _ := e.post(e.c, "/settings/backup", url.Values{"op": {"schedule"}, "every": {"hourly"}, "hour": {"3"}, "weekday": {"0"}}, "/settings/backup"); code != 400 {
		t.Fatalf("bad every %d", code)
	}
	if len(f.planCalls) != 0 {
		t.Fatalf("agent asked %v", f.planCalls)
	}
	if loc := post(url.Values{"op": {"schedule"}, "every": {"daily"}, "hour": {"3"}, "weekday": {"0"}, "password": {pw}, "passphrase": {pass}, "passphrase2": {pass}}); loc != "/settings/backup?done=schedule#plan" {
		t.Fatalf("schedule %s", loc)
	}
	if loc := post(url.Values{"op": {"dest-save"}, "kind": {"s3"}, "name": {"S3"}, "keep": {"7"}, "on": {"1"}, "endpoint": {"https://is3.cloudhost.id"}, "bucket": {"nostek-bk"},
		"access_key": {"AKTEST"}, "secret": {"top-secret-key"}, "password": {pw}}); loc != "/settings/backup?done=dest#dests" {
		t.Fatalf("dest %s", loc)
	}
	if loc := post(url.Values{"op": {"dest-save"}, "kind": {"s3"}, "name": {"S3"}, "keep": {"7"}, "bucket": {"Bad"}, "password": {pw}}); !strings.Contains(loc, "bucket+name") {
		t.Fatalf("bad dest %s", loc)
	}
	if loc := post(url.Values{"op": {"dest-test"}, "id": {"d0000aaaa"}}); loc != "/settings/backup?done=desttest#dests" {
		t.Fatalf("test %s", loc)
	}
	if loc := post(url.Values{"op": {"dest-test"}, "id": {"d0000bbbb"}}); !strings.Contains(loc, "Permission+denied") {
		t.Fatalf("test failed %s", loc)
	}
	if loc := post(url.Values{"op": {"ssh-key"}}); loc != "/settings/backup?done=sshkey#dests" {
		t.Fatalf("ssh key %s", loc)
	}
	if loc := post(url.Values{"op": {"send"}, "name": {sbkName}, "dest": {"d0000aaaa"}}); loc != "/settings/backup?job=j5bc0000000000003#job" {
		t.Fatalf("send %s", loc)
	}
	if code, _, _ := e.post(e.c, "/settings/backup", url.Values{"op": {"send"}, "name": {"../x"}, "dest": {"d0000aaaa"}}, "/settings/backup"); code != 400 {
		t.Fatalf("bad send %d", code)
	}
	if loc := post(url.Values{"op": {"dest-delete"}, "id": {"d0000aaaa"}}); loc != "/settings/backup?done=destdelete#dests" {
		t.Fatalf("delete %s", loc)
	}
	all := strings.Join(f.planCalls, "\n")
	for _, want := range []string{`/v1/backup-plan/schedule {"every":"daily","hour":3,"passphrase":"` + pass + `","weekday":0}`, `"secret":"top-secret-key"`,
		"/v1/server-backups/" + sbkName + `/send {"dest":"d0000aaaa"}`, "/v1/backup-plan/dests/d0000aaaa/delete"} {
		if !strings.Contains(all, want) {
			t.Errorf("agent calls lack %s:\n%s", want, all)
		}
	}
	audit := e.auditText()
	if strings.Contains(audit, pass) || strings.Contains(audit, "top-secret-key") {
		t.Fatalf("secret in the audit log:\n%s", audit)
	}
	for _, want := range []string{"server.backup.schedule daily 3:00 WIB, new passphrase", "server.backup.schedule refused: wrong password", "server.backup.dest d0000aaaa s3 S3", "server.backup.dest.test d0000bbbb failed", "server.backup.send"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q:\n%s", want, audit)
		}
	}

	// the loop: nothing when not due; when due the database copy is written first
	f.planCalls, f.plan.Due = nil, false
	e.srv.BackupTick(t.Context())
	if len(f.planCalls) != 0 {
		t.Fatal("ran when not due")
	}
	f.plan.Due = true
	e.srv.BackupTick(t.Context())
	if len(f.planCalls) != 1 || !strings.HasPrefix(f.planCalls[0], "/v1/backup-plan/run") || !strings.HasPrefix(string(f.sbkSeen), "SQLite format 3") {
		t.Fatalf("%v %q", f.planCalls, f.sbkSeen)
	}
}

func TestServerBackupAlert(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.srv.SetAlertKey(alert.NewKey([]byte("0123456789abcdef0123456789abcdef")))
	e.srv.alertSync = true
	got := fakeTelegram(t)
	sealed, _ := e.srv.akey.Seal(testTok)
	e.st.SetSetting(kTgChat, "1")
	e.st.SetSetting(kTgToken, sealed)
	e.st.SetSetting(kKindsOff, "disk,memory")
	e.st.SetSetting(kWatchSeq, "1")
	e.srv.cfg.DefaultLang = "en"
	f.watch = agent.WatchView{Seq: 3, Events: []agent.WatchEvent{{Seq: 2, Service: "server-backup", Kind: "failed", Reason: "backup"},
		{Seq: 3, Service: "server-backup", Kind: "failed", Reason: "copy"}}}
	e.srv.AlertTick(t.Context())
	n := 0
	for _, m := range *got {
		if strings.Contains(m, "scheduled server backup failed") || strings.Contains(m, "copying it to a destination failed") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%q", *got)
	}
}
