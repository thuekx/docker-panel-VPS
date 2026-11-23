package sites

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func restarts(f *fakeRunner, what string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "restart") && strings.HasSuffix(j, what) {
			n++
		}
	}
	return n
}

func TestWatchdogRestartsUnhealthy(t *testing.T) {
	m, f, id := readySite(t)
	f.calls = nil
	f.ps = `{"Service":"web","State":"running","Health":"unhealthy"}` + "\n" + `{"Service":"db","State":"running","Health":"healthy"}`
	f.answer = map[string]string{"label=com.docker.compose.project=" + CoreProject: `{"Names":"nostek-core-edge-1","State":"running","Status":"Up 2 hours (healthy)","Labels":"com.docker.compose.project=nostek-core,com.docker.compose.service=edge"}` + "\n" +
		`{"Names":"nostek-core-panel-1","State":"exited","Status":"Exited (1)","Labels":"com.docker.compose.service=panel,com.docker.compose.project=nostek-core"}` + "\n" +
		`{"Names":"nostek-core-docker-api-1","State":"exited","Status":"Exited (1)","Labels":"com.docker.compose.service=docker-api"}`}
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ { // two bad checks: nothing yet
		m.WatchTick(context.Background(), t0.Add(time.Duration(i)*time.Minute))
	}
	if n := restarts(f, " web"); n != 0 {
		t.Fatalf("restarted after 2 checks: %d", n)
	}
	m.WatchTick(context.Background(), t0.Add(2*time.Minute))
	if restarts(f, " web") != 1 || restarts(f, "nostek-core-panel-1") != 1 || restarts(f, "docker-api-1") != 0 || restarts(f, " db") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
	// still bad: two more restarts within the hour, then it gives up once
	for i := 3; i < 20; i++ {
		m.WatchTick(context.Background(), t0.Add(time.Duration(i)*time.Minute))
	}
	if n := restarts(f, " web"); n != watchMaxRestarts {
		t.Fatalf("web restarts %d", n)
	}
	v := m.Watchdog(0)
	kinds := map[string]int{}
	for _, e := range v.Events {
		kinds[e.Site+"/"+e.Service+" "+e.Kind]++
		if e.Reason == "" {
			t.Errorf("no reason: %+v", e)
		}
	}
	if kinds[id+"/web restarted"] != 3 || kinds[id+"/web gave_up"] != 1 || kinds["/panel restarted"] != 3 || kinds["/panel gave_up"] != 1 {
		t.Fatalf("%v", kinds)
	}
	// healthy again: one recovery; events after a sequence number only
	seq := v.Seq
	f.ps = `{"Service":"web","State":"running","Health":"healthy"}`
	f.answer = nil
	m.WatchTick(context.Background(), t0.Add(30*time.Minute))
	v = m.Watchdog(seq)
	if len(v.Events) != 1 || v.Events[0].Kind != "recovered" || v.Events[0].Service != "web" {
		t.Fatalf("%+v", v)
	}
	m.WatchTick(context.Background(), t0.Add(31*time.Minute))
	if len(m.Watchdog(v.Seq).Events) != 0 {
		t.Error("recovered twice")
	}
	// kept across a restart of the agent, private to it
	m2 := New(m.cfg, f, nil)
	if got := m2.Watchdog(0); got.Seq != v.Seq || len(got.Events) == 0 {
		t.Errorf("not kept: %+v", got.Seq)
	}
	if fi, err := os.Stat(filepath.Join(m.cfg.Data, "watchdog.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("watchdog.json %v %v", fi, err)
	}
}

func TestWatchdogSkipsBusyAndNative(t *testing.T) {
	m, f, _ := readySite(t)
	f.ps = `{"Service":"web","State":"exited","Health":""}`
	m.mu.Lock() // a job holds the change lock: the website is left alone
	for i := 0; i < 5; i++ {
		m.WatchTick(context.Background(), time.Now())
	}
	m.mu.Unlock()
	if n := restarts(f, " web"); n != 0 {
		t.Fatalf("restarted while busy: %d", n)
	}

	mn, _, h, _, s := nativeReady(t)
	h.mu.Lock()
	h.active[NativeUnit(s.ID)] = false
	h.active["nginx"], h.active["mariadb"] = true, true
	h.mu.Unlock()
	for i := 0; i < 3; i++ {
		mn.WatchTick(context.Background(), time.Now())
	}
	if !strings.Contains(h.all(), "systemctl restart "+NativeUnit(s.ID)) {
		t.Fatalf("native php not restarted:\n%s", h.all())
	}
	if strings.Contains(h.all(), "systemctl restart nginx") {
		t.Error("nginx restarted while active")
	}
	if ev := mn.Watchdog(0).Events; len(ev) != 1 || ev[0].Site != s.ID || ev[0].Service != "php" || ev[0].Kind != "restarted" {
		t.Errorf("%+v", ev)
	}
}

func TestUsage(t *testing.T) {
	m, f, id := readySite(t)
	proc := t.TempDir()
	old := procDir
	procDir = proc
	t.Cleanup(func() { procDir = old })
	write := func(stat string) {
		_ = os.WriteFile(filepath.Join(proc, "stat"), []byte(stat), 0o644)
	}
	write("cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 50 0 50 400 0 0 0\ncpu1 50 0 50 400 0 0 0\n")
	_ = os.WriteFile(filepath.Join(proc, "meminfo"), []byte("MemTotal: 4000000 kB\nMemAvailable: 3000000 kB\n"), 0o644)
	_ = os.WriteFile(filepath.Join(proc, "loadavg"), []byte("0.50 0.40 0.30 1/200 999\n"), 0o644)
	p := Project(id)
	f.stats = `{"Name":"` + p + `-web-1","CPUPerc":"12.50%","MemUsage":"100MiB / 512MiB"}` + "\n" +
		`{"Name":"` + p + `-db-1","CPUPerc":"2.50%","MemUsage":"50MiB / 512MiB"}` + "\n" +
		`{"Name":"other-web-1","CPUPerc":"90%","MemUsage":"1GiB / 2GiB"}`
	t0 := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	m.UsageTick(context.Background(), t0)
	write("cpu  200 0 200 1400 0 0 0 0 0 0\ncpu0 100 0 100 700 0 0 0\ncpu1 100 0 100 700 0 0 0\n")
	m.UsageTick(context.Background(), t0.Add(5*time.Minute))
	m.UsageTick(context.Background(), t0.Add(65*time.Minute))
	v, err := m.Usage(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Recent) != 3 || v.Recent[0].CPU != 15 || v.Recent[0].Mem != 150<<20 || len(v.Hours) != 2 || v.CPUs != 2 || v.MemMax == 0 {
		t.Fatalf("%+v", v)
	}
	sv, _ := m.Usage("")
	if len(sv.Recent) != 3 || sv.Recent[0].CPU != 0 || sv.Recent[1].CPU != 50 || sv.Recent[1].Mem != 1000000<<10 || sv.Recent[1].Load != 0.5 || sv.MemMax != 4000000<<10 {
		t.Fatalf("server %+v", sv)
	}
	if sv.Hours[0].CPU != 25 { // the average of the hour's two samples
		t.Errorf("hour %+v", sv.Hours[0])
	}
	if _, err := m.Usage("nope"); err == nil {
		t.Error("unknown website")
	}
	m.dropStats(id)
	if _, err := os.Stat(m.usagePath(id)); !os.IsNotExist(err) {
		t.Error("usage file kept")
	}
}
