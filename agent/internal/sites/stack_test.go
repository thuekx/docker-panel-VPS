package sites

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"45.5MiB": 47710208, "1GiB": 1 << 30, "512MiB": 512 << 20, "0B": 0, "12.3kB": 12300, "1.5GB": 1500000000, "junk": -1, "": -1} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStackReadsContainersAndUsage(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	c := req()
	c.Server = "nginx"
	job, _, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	f.ps = `{"ID":"aaa111","Name":"np-site-voltix-id-db-1","Service":"db","Image":"mariadb:11.8@sha256:abc","State":"running","Health":"healthy","Status":"Up 2 hours (healthy)"}
{"ID":"bbb222","Name":"np-site-voltix-id-web-1","Service":"web","Image":"nginx:1.30-alpine@sha256:def","State":"running","Health":"","Status":"Up 2 hours"}
{"ID":"ccc333","Name":"np-site-voltix-id-php-1","Service":"php","Image":"wordpress:php8.4-fpm","State":"exited","Health":"","Status":"Exited (1) 3 minutes ago"}
{"ID":"ddd444","Name":"np-site-voltix-id-cli-run-1","Service":"cli","Image":"wordpress:cli","State":"exited","Health":"","Status":"Exited (0)"}`
	f.ins = `[{"Id":"aaa111ffff","RestartCount":0,"State":{"Running":true,"StartedAt":"2026-11-11T09:45:00.123456789Z"}},
{"Id":"bbb222ffff","RestartCount":2,"State":{"Running":true,"StartedAt":"2026-11-11T09:46:00Z"}},
{"Id":"ccc333ffff","RestartCount":5,"State":{"Running":false,"StartedAt":"2026-11-11T09:46:00Z"}}]`
	f.stats = `{"ID":"aaa111ffff","CPUPerc":"0.35%","MemUsage":"98.5MiB / 512MiB"}
{"ID":"bbb222ffff","CPUPerc":"0.01%","MemUsage":"4.2MiB / 128MiB"}`
	st, err := m.Stack(context.Background(), "voltix-id")
	if err != nil {
		t.Fatal(err)
	}
	if st.Project != "np-site-voltix-id" || len(st.Containers) != 3 {
		t.Fatalf("stack: %+v", st)
	}
	var order []string
	for _, c := range st.Containers {
		order = append(order, c.Service)
	}
	if strings.Join(order, ",") != "web,php,db" {
		t.Fatalf("order %v (cli must be left out)", order)
	}
	web, php, db := st.Containers[0], st.Containers[1], st.Containers[2]
	if web.Image != "nginx:1.30-alpine" || web.Restarts != 2 || web.CPU != 0.01 || web.MemBytes != 4404019 || web.MemLimit != 128<<20 || web.Started == 0 {
		t.Fatalf("web: %+v", web)
	}
	if php.State != "exited" || php.CPU != -1 || php.MemBytes != -1 || php.Started != 0 || php.Restarts != 5 {
		t.Fatalf("php: %+v", php)
	}
	if db.Health != "healthy" || db.CPU != 0.35 || db.Image != "mariadb:11.8" {
		t.Fatalf("db: %+v", db)
	}
	if st2, up, total := m.StatusCount(context.Background(), "voltix-id"); st2 != "partial" || up != 2 || total != 4 {
		t.Fatalf("status count: %s %d/%d", st2, up, total)
	}
	f.fail = " ps "
	if _, err := m.Stack(context.Background(), "voltix-id"); err == nil {
		t.Fatal("a docker failure must be an error, never an empty healthy stack")
	}
	if _, err := m.Stack(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown site")
	}
}

func TestRestartServiceOnlyKnownServices(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	id, _, err := m.Create(context.Background(), req()) // apache: web + db
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, id)
	for _, bad := range []string{"php", "cli", "edge", "../x", ""} {
		if err := m.RestartService(context.Background(), "voltix-id", bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("service %q accepted: %v", bad, err)
		}
	}
	f.calls = nil
	if err := m.RestartService(context.Background(), "voltix-id", "db"); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	if !strings.HasSuffix(last, " restart db") || !strings.Contains(last, "-p np-site-voltix-id") {
		t.Fatalf("restart call: %s", last)
	}
	if err := m.RestartService(context.Background(), "nope", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown site")
	}
}
