package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func groupEnv(t *testing.T) (*Manager, *fakeRunner, *fakeHost, string) {
	t.Helper()
	m, f, id := readySite(t)
	h := &fakeHost{}
	m.UseFirewall(h)
	m.srvEtc = t.TempDir()
	m.cgroupFS = t.TempDir()
	m.cpuFn = func() int { return 2 }
	m.memFn = func() int { return 4096 }
	return m, f, h, id
}

func hostCalls(h *fakeHost) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.calls, "\n")
}

// serviceCount counts the services of a rendered compose file.
func serviceCount(cf string) int {
	seen := map[string]bool{}
	eachService(cf, func(_ int, svc, _ string) { seen[svc] = true })
	return len(seen)
}

func TestGroupLifecycle(t *testing.T) {
	m, f, h, id := groupEnv(t)
	ctx := context.Background()
	l := GroupLimits{CPU: 150, Memory: 1024, Tasks: 512}
	if err := m.SetGroup(ctx, "a12", l); err != nil {
		t.Fatal(err)
	}
	unit := mustRead(t, filepath.Join(m.srvEtc, "systemd", "system", "npgroup-a12.slice"))
	for _, want := range []string{"[Slice]", "CPUQuota=150%", "MemoryMax=1024M", "MemorySwapMax=0", "TasksMax=512"} {
		if !strings.Contains(unit, want) {
			t.Errorf("slice unit lacks %q:\n%s", want, unit)
		}
	}
	c := hostCalls(h)
	if !strings.Contains(c, "systemctl daemon-reload") || !strings.Contains(c, "systemctl set-property --runtime npgroup-a12.slice CPUQuota=150% MemoryMax=1024M MemorySwapMax=0 TasksMax=512") ||
		!strings.Contains(c, "systemctl start npgroup-a12.slice") {
		t.Errorf("limits not applied to the running slice:\n%s", c)
	}

	// into the group: every service of the website runs in its slice
	if err := m.SetSiteGroup(ctx, id, "a12"); err != nil {
		t.Fatal(err)
	}
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	if n, want := strings.Count(cf, "cgroup_parent: npgroup-a12.slice"), serviceCount(cf); n != want || n == 0 {
		t.Errorf("%d of %d services in the group:\n%s", n, want, cf)
	}
	if !strings.Contains(calls(f), "up -d --wait") {
		t.Errorf("containers not recreated in the group: %s", calls(f))
	}
	if s, _ := m.Get(id); s.Group != "a12" {
		t.Errorf("site.json group %q", s.Group)
	}

	// memory in use, read from the slice's cgroup
	dir := filepath.Join(m.cgroupFS, "npgroup.slice", "npgroup-a12.slice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.current"), []byte("314572800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := m.Groups()
	if err != nil || len(g) != 1 || g[0].Name != "a12" || g[0].GroupLimits != l || len(g[0].Sites) != 1 || g[0].Sites[0] != id || g[0].MemUsed != 300 {
		t.Fatalf("groups %+v %v", g, err)
	}

	// a group with a website cannot go; out of the group, it can
	if err := m.DeleteGroup(ctx, "a12"); !errors.Is(err, ErrBadInput) {
		t.Fatalf("delete with a website: %v", err)
	}
	if err := m.SetSiteGroup(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml")); strings.Contains(cf, "cgroup_parent") {
		t.Errorf("still in the group:\n%s", cf)
	}
	if err := m.DeleteGroup(ctx, "a12"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.srvEtc, "systemd", "system", "npgroup-a12.slice")); !os.IsNotExist(err) {
		t.Error("slice unit left behind")
	}
	if g, _ := m.Groups(); len(g) != 0 {
		t.Errorf("groups after delete %+v", g)
	}
}

func TestGroupRefused(t *testing.T) {
	m, _, _, id := groupEnv(t)
	ctx := context.Background()
	ok := GroupLimits{CPU: 100, Memory: 512, Tasks: 256}
	for name, l := range map[string]GroupLimits{
		"Bad-Name":              ok,
		"":                      ok,
		strings.Repeat("a", 25): ok,
		"lowcpu":                {CPU: 5, Memory: 512, Tasks: 256},
		"bigcpu":                {CPU: 201, Memory: 512, Tasks: 256}, // 2 cores
		"lowmem":                {CPU: 100, Memory: 64, Tasks: 256},
		"bigmem":                {CPU: 100, Memory: 4097, Tasks: 256},
		"lowtasks":              {CPU: 100, Memory: 512, Tasks: 10},
	} {
		if err := m.SetGroup(ctx, name, l); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q %+v: %v", name, l, err)
		}
	}
	if err := m.SetSiteGroup(ctx, id, "none"); !errors.Is(err, ErrBadInput) {
		t.Errorf("unknown group: %v", err)
	}
	if err := m.DeleteGroup(ctx, "none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete unknown: %v", err)
	}
}

func TestGroupPutBackOnFailure(t *testing.T) {
	m, _, h, _ := groupEnv(t)
	ctx := context.Background()
	if err := m.SetGroup(ctx, "a1", GroupLimits{CPU: 100, Memory: 512, Tasks: 256}); err != nil {
		t.Fatal(err)
	}
	h.fail = "set-property"
	if err := m.SetGroup(ctx, "a1", GroupLimits{CPU: 200, Memory: 1024, Tasks: 512}); err == nil {
		t.Fatal("failure not reported")
	}
	if unit := mustRead(t, filepath.Join(m.srvEtc, "systemd", "system", "npgroup-a1.slice")); !strings.Contains(unit, "MemoryMax=512M") {
		t.Errorf("previous limits not put back:\n%s", unit)
	}
	if g, _ := m.Groups(); len(g) != 1 || g[0].Memory != 512 {
		t.Errorf("groups.json changed: %+v", g)
	}
	if err := m.SetGroup(ctx, "a2", GroupLimits{CPU: 100, Memory: 512, Tasks: 256}); err == nil {
		t.Fatal("failure not reported")
	}
	if _, err := os.Stat(filepath.Join(m.srvEtc, "systemd", "system", "npgroup-a2.slice")); !os.IsNotExist(err) {
		t.Error("new slice unit left after a failure")
	}
}

func TestGroupNative(t *testing.T) {
	s := Site{ID: "n-example-test", PHP: "8.4", Group: "a7"}
	p := NativePaths{Etc: "/etc", Run: "/run", Data: "/srv/nostek"}
	if unit := s.NativeService(p); !strings.Contains(unit, "\nSlice=npgroup-a7.slice\n") {
		t.Errorf("native service not in the group:\n%s", unit)
	}
	if !strings.Contains(strings.Join(s.nativeSandbox(p), " "), "Slice=npgroup-a7.slice") {
		t.Error("WP-CLI runs not in the group")
	}
	s.Group = ""
	if strings.Contains(s.NativeService(p), "Slice=") {
		t.Error("slice without a group")
	}
}
