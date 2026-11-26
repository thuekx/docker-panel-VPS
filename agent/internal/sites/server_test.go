package sites

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const corePS = `{"Names":"nostek-core-edge-1","State":"running","Status":"Up 3 hours (healthy)","Labels":"com.docker.compose.project=nostek-core,com.docker.compose.service=edge"}
{"Names":"nostek-core-panel-1","State":"running","Status":"Up 3 hours (unhealthy)","Labels":"com.docker.compose.project=nostek-core,com.docker.compose.service=panel"}
{"Names":"nostek-core-waf-1","State":"exited","Status":"Exited (1) 2 minutes ago","Labels":"com.docker.compose.project=nostek-core,com.docker.compose.service=waf"}`

func coreEnv(t *testing.T) (*Manager, *fakeRunner, *fwHost) {
	t.Helper()
	m, f := newTest(t, nil)
	h := &fwHost{}
	m.UseFirewall(h)
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, "label=com.docker.compose.service=panel"):
			return []byte("nostek-core-panel-1\n"), nil, true
		case strings.Contains(j, "label=com.docker.compose.project=nostek-core"):
			return []byte(corePS), nil, true
		}
		return nil, nil, false
	}
	return m, f, h
}

func TestCoreServices(t *testing.T) {
	m, f, h := coreEnv(t)
	ctx := context.Background()
	got := map[string]CoreService{}
	for _, c := range m.CoreServices(ctx) {
		got[c.Name] = c
	}
	if c := got["edge"]; !c.OK || c.Kind != "container" || !c.Restart {
		t.Fatalf("edge %+v", c)
	}
	if got["panel"].OK || got["waf"].OK || got["waf"].State != "exited" {
		t.Fatalf("%+v %+v", got["panel"], got["waf"])
	}
	// the fake host answers nothing to systemctl is-active: not active
	if c := got["docker"]; c.Kind != "unit" || c.OK || c.Restart {
		t.Fatalf("docker %+v", c)
	}
	if _, ok := got["nginx"]; ok {
		t.Fatal("nginx without the native engine")
	}
	if err := m.RestartCore(ctx, "panel"); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if strings.Join(last, " ") != "restart nostek-core-panel-1" {
		t.Fatalf("%v", last)
	}
	if err := m.RestartCore(ctx, "docker"); !errors.Is(err, ErrBadInput) {
		t.Fatal("docker restarted", err)
	}
	if err := m.RestartCore(ctx, "nginx"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.RestartCore(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.RestartCore(ctx, "fail2ban"); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartCore(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(h.calls, "\n")
	if !strings.Contains(calls, "systemctl restart fail2ban.service") || !strings.Contains(calls, "--on-active=3 /usr/bin/systemctl restart nostek-agent.service") {
		t.Fatal(calls)
	}
}

func writeProc(t *testing.T, dir string, pid int, comm string, ticks, rssPages int, uid, cg string) {
	t.Helper()
	d := filepath.Join(dir, fmt.Sprint(pid))
	_ = os.MkdirAll(d, 0o755)
	// fields after "(comm)": state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime prio nice threads itreal start vsize rss
	stat := fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 100 1000 %d\n", pid, comm, ticks, rssPages)
	_ = os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644)
	_ = os.WriteFile(filepath.Join(d, "status"), []byte("Name:\t"+comm+"\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(d, "cgroup"), []byte(cg), 0o644)
}

func TestProcesses(t *testing.T) {
	m, f := newTest(t, nil)
	dir := t.TempDir()
	old, oldWait := procDir, procWait
	procDir, procWait = dir, 10*time.Millisecond
	defer func() { procDir, procWait, procBetween = old, oldWait, nil }()
	id := strings.Repeat("ab", 32)
	f.answer = map[string]string{"--no-trunc": id + " np-site-voltix-id-php-1\n"}
	_ = os.WriteFile(filepath.Join(dir, "meminfo"), []byte("MemTotal:        4000000 kB\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "stat"), []byte("cpu  1 2 3\ncpu0 1 2 3\ncpu1 1 2 3\n"), 0o644)
	writeProc(t, dir, 10, "php-fpm: pool www", 100, 25000, "33", "0::/system.slice/docker-"+id+".scope\n")
	writeProc(t, dir, 20, "mariadbd", 100, 100000, "999", "0::/system.slice/mariadb.service\n")
	writeProc(t, dir, 30, "kthreadd", 100, 0, "0", "0::/\n")
	_ = os.MkdirAll(filepath.Join(dir, "self"), 0o755)
	procBetween = func() {
		writeProc(t, dir, 10, "php-fpm: pool www", 150, 25000, "33", "0::/system.slice/docker-"+id+".scope\n")
	}
	v, err := m.Processes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Cores != 2 || v.MemTotal != 4000000<<10 || v.Count != 2 {
		t.Fatalf("%+v", v)
	}
	top := v.ByCPU[0]
	if top.PID != 10 || top.Name != "php-fpm: pool www" || top.Where != "np-site-voltix-id-php-1" || top.CPU < 50 {
		t.Fatalf("%+v", top)
	}
	if v.ByMem[0].PID != 20 || v.ByMem[0].Where != "mariadb.service" || v.ByMem[0].Mem <= 0 {
		t.Fatalf("%+v", v.ByMem[0])
	}
	for _, p := range v.ByCPU {
		if p.PID == 30 {
			t.Fatal("kernel thread listed")
		}
	}
}

func TestLineDiff(t *testing.T) {
	d, add, del := lineDiff("a\nb\nc\n", "a\nB\nc\nd\n")
	if add != 2 || del != 1 || !strings.Contains(d, "- b") || !strings.Contains(d, "+ B") || !strings.Contains(d, "+ d") {
		t.Fatalf("%q %d %d", d, add, del)
	}
	if d, a, r := lineDiff("x\n", "x\n"); d != "" || a != 0 || r != 0 {
		t.Fatal(d)
	}
}

func TestDrift(t *testing.T) {
	m, _, id := readySite(t)
	ctx := context.Background()
	ds, err := m.DriftCheck()
	if err != nil || len(ds) != 1 || ds[0].Drift {
		t.Fatalf("fresh website drifts: %+v %v", ds, err)
	}
	// a hand edit of php.ini and a removed compose file
	conf := filepath.Join(m.dir(id), "php.ini")
	b, _ := os.ReadFile(conf)
	_ = os.WriteFile(conf, append(b, []byte("# edited by hand\n")...), 0o644)
	ds, _ = m.DriftCheck()
	var ini DriftFile
	for _, f := range ds[0].Files {
		if f.File == "php.ini" {
			ini = f
		}
	}
	if !ds[0].Drift || ini.State != "changed" || !strings.Contains(ini.Diff, "- # edited by hand") || ini.Removed != 1 {
		t.Fatalf("%+v", ds[0])
	}
	if err := m.DriftFix(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ds, _ = m.DriftCheck(); ds[0].Drift {
		t.Fatalf("still drifts: %+v", ds[0])
	}
	_ = os.Remove(filepath.Join(m.dir(id), "compose.yml"))
	ds, _ = m.DriftCheck()
	if !ds[0].Drift || ds[0].Files[0].File != "compose.yml" || ds[0].Files[0].State != "missing" {
		t.Fatalf("%+v", ds[0].Files)
	}
	if err := m.DriftFix(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDriftNginxFromTemplate(t *testing.T) {
	m, _ := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	r := req()
	r.Server = "nginx"
	job, site, err := m.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	conf := filepath.Join(m.dir(site.ID), "nginx.conf")
	b, _ := os.ReadFile(conf)
	_ = os.WriteFile(conf, []byte(strings.Replace(string(b), "server {", "server {\n    autoindex on;", 1)), 0o644)
	ds, _ := m.DriftCheck()
	found := false
	for _, f := range ds[0].Files {
		if f.File == "nginx.conf" && f.State == "changed" && strings.Contains(f.Diff, "- ") && strings.Contains(f.Diff, "autoindex on;") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", ds[0].Files)
	}
	if err := m.DriftFix(context.Background(), site.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(conf); strings.Contains(string(b), "autoindex on;") {
		t.Fatal("not written back")
	}
}
