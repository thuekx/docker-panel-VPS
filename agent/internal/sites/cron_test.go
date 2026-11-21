package sites

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, wib); return v }
	for _, c := range []struct {
		spec, when string
		due        bool
	}{
		{"* * * * *", "2026-11-22 10:07", true},
		{"*/15 * * * *", "2026-11-22 10:30", true},
		{"*/15 * * * *", "2026-11-22 10:31", false},
		{"0 3 * * *", "2026-11-22 03:00", true},
		{"0 3 * * 1-5", "2026-11-22 03:00", false}, // Sunday
		{"0 3 * * 0", "2026-11-22 03:00", true},
		{"0 3 * * 7", "2026-11-22 03:00", true},
		{"0 3 1 * 0", "2026-11-22 03:00", true}, // day of month OR weekday
		{"0 3 1 * *", "2026-11-22 03:00", false},
		{"5,10 8-9 * 11 *", "2026-11-22 09:10", true},
		{"@daily", "2026-11-22 00:00", true},
		{"@hourly", "2026-11-22 10:01", false},
		{"30 2/6 * * *", "2026-11-22 14:30", true},
	} {
		s, err := parseCron(c.spec)
		if err != nil || s.due(at(c.when)) != c.due {
			t.Errorf("%q at %s: %v %v", c.spec, c.when, s.due(at(c.when)), err)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "a * * * *", "5-1 * * * *", "* * * * * *", "@reboot"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	s, _ := parseCron("0 3 * * *")
	if n := s.next(at("2026-11-22 03:00")); !n.Equal(at("2026-11-23 03:00")) {
		t.Errorf("next %v", n)
	}
}

func TestCronJobs(t *testing.T) {
	m, f, id := readySite(t)
	for _, bad := range []CronChange{
		{Op: "add", Schedule: "* * *", Command: "php x.php"},
		{Op: "add", Schedule: "* * * * *", Command: " "},
		{Op: "add", Schedule: "* * * * *", Command: "a\nb"},
		{Op: "add", Schedule: "* * * * *", Command: strings.Repeat("x", cronCmdMax+1)},
		{Op: "remove", ID: "nope"},
		{Op: "launch"},
	} {
		if _, err := m.ChangeCron(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	jid, err := m.ChangeCron(id, CronChange{Op: "add", Schedule: " */5   *  * * * ", Command: "php /var/www/html/task.php --x 'a b'"})
	if err != nil || len(jid) != 6 {
		t.Fatal(jid, err)
	}
	s, _ := m.Get(id)
	if len(s.Crons) != 1 || s.Crons[0].Schedule != "*/5 * * * *" {
		t.Fatalf("%+v", s.Crons)
	}
	if _, err := m.ChangeCron(id, CronChange{Op: "on", ID: jid}); !errors.Is(err, ErrNoChange) {
		t.Errorf("on twice: %v", err)
	}
	// a tick at a due minute runs it in the website's PHP container as www-data, a minute later it does not
	var wg sync.WaitGroup
	f.calls = nil
	tick := time.Date(2026, 11, 22, 10, 5, 0, 0, wib)
	if n := m.CronTick(context.Background(), tick, &wg); n != 1 {
		t.Fatalf("started %d", n)
	}
	wg.Wait()
	if n := m.CronTick(context.Background(), tick.Add(time.Minute), &wg); n != 0 {
		t.Errorf("not due: %d", n)
	}
	if all := calls(f); !strings.Contains(all, "exec -T -u 33:33 web sh -c php /var/www/html/task.php --x 'a b'") {
		t.Errorf("calls:\n%s", all)
	}
	v, err := m.Cron(id)
	if err != nil || !v.Supported || v.Where != "web" || len(v.Jobs) != 1 || len(v.Jobs[0].Runs) != 1 || v.Jobs[0].Runs[0].Code != 0 || v.Jobs[0].Next == 0 {
		t.Fatalf("%+v %v", v, err)
	}
	// a failing run keeps its exit code and output; only the last runs are kept, newest first
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "sh -c") {
			return []byte("PHP Fatal error: boom"), errors.New("exit status 2"), true
		}
		return nil, nil, false
	}
	for i := 0; i < cronRuns+1; i++ {
		m.CronTick(context.Background(), tick, &wg)
		wg.Wait()
	}
	v, _ = m.Cron(id)
	if r := v.Jobs[0].Runs; len(r) != cronRuns || r[0].Code != -1 || !strings.Contains(r[0].Output, "boom") {
		t.Errorf("runs %+v", r)
	}
	// run now, off, edit, remove
	f.hook = nil
	if _, err := m.ChangeCron(id, CronChange{Op: "run", ID: jid}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if v, _ = m.Cron(id); len(v.Jobs[0].Runs) > 0 && v.Jobs[0].Runs[0].Manual {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !v.Jobs[0].Runs[0].Manual || v.Jobs[0].Runs[0].Code != 0 {
		t.Errorf("manual run %+v", v.Jobs[0].Runs[0])
	}
	if _, err := m.ChangeCron(id, CronChange{Op: "off", ID: jid}); err != nil {
		t.Fatal(err)
	}
	if n := m.CronTick(context.Background(), tick, &wg); n != 0 {
		t.Error("a job that is off ran")
	}
	if _, err := m.ChangeCron(id, CronChange{Op: "edit", ID: jid, Schedule: "@daily", Command: "php a.php"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ChangeCron(id, CronChange{Op: "edit", ID: jid, Schedule: "@daily", Command: "php a.php"}); !errors.Is(err, ErrNoChange) {
		t.Errorf("same edit: %v", err)
	}
	if _, err := m.ChangeCron(id, CronChange{Op: "remove", ID: jid}); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Get(id); s.Crons != nil {
		t.Errorf("left %+v", s.Crons)
	}
	if v, _ := m.Cron(id); len(v.Jobs) != 0 {
		t.Errorf("view %+v", v)
	}
}

func TestCronWhere(t *testing.T) {
	for _, c := range []struct {
		s         Site
		svc, user string
	}{
		{Site{Server: "nginx", PHP: "8.4"}, "php", "33:33"},
		{Site{Server: "apache", PHP: "8.4"}, "web", "33:33"},
		{Site{App: "laravel", Server: "nginx"}, "php", "33:33"},
		{Site{App: "node"}, "app", ""},
		{Site{App: "n8n"}, "app", ""},
	} {
		if svc, u := cronService(c.s); svc != c.svc || u != c.user {
			t.Errorf("%+v: %s %s", c.s, svc, u)
		}
	}
	if cronSupported(Site{App: "static"}) {
		t.Error("static")
	}
}

func TestCronNative(t *testing.T) {
	m, _, h, _, s := nativeReady(t)
	jid, err := m.ChangeCron(s.ID, CronChange{Op: "add", Schedule: "* * * * *", Command: "php -r 'echo 1;'"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	m.CronTick(context.Background(), time.Now(), &wg)
	wg.Wait()
	all := h.all()
	if !strings.Contains(all, "systemd-run") || !strings.Contains(all, "--uid="+NativeUser(s.ID)) || !strings.Contains(all, "-- /bin/sh -c php -r 'echo 1;'") {
		t.Errorf("host calls:\n%s", all)
	}
	if v, _ := m.Cron(s.ID); v.Where != "native" || len(v.Jobs) != 1 || v.Jobs[0].ID != jid || len(v.Jobs[0].Runs) != 1 {
		t.Errorf("%+v", v)
	}
}
