package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func limitsOfSite(t *testing.T, m *Manager, id string) map[string]ServiceRAM {
	t.Helper()
	l, err := m.Limits(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ServiceRAM{}
	for _, v := range l.Services {
		out[v.Service] = v
	}
	return out
}

func TestLimitsStandard(t *testing.T) {
	m, _, id := readySite(t)
	m.memFn = func() int { return 4096 }
	l, err := m.Limits(id)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ServiceRAM{}
	for _, v := range l.Services {
		got[v.Service] = v
	}
	if v := got["db"]; v.Standard != 512 || v.Current != 512 || v.PHP {
		t.Errorf("db %+v", v)
	}
	if v := got["web"]; v.Standard != 512 || !v.PHP {
		t.Errorf("web (Apache runs PHP) %+v", v)
	}
	if l.PHP == nil || *l.PHP != PHPStandard || l.HostMB != 4096 || l.TotalMB != 1024 {
		t.Errorf("limits %+v (total counts db + web, not WP-CLI)", l)
	}
	if s, _ := m.Get(id); s.PHPIniFile() != PHPIni {
		t.Error("standard php.ini changed")
	}
}

func TestSetLimitsRaisesAndGoesBack(t *testing.T) {
	m, f, id := readySite(t)
	dir := m.dir(id)
	if err := m.SetLimits(context.Background(), id, LimitChange{RAM: map[string]int{"db": 1024}}); err != nil {
		t.Fatal(err)
	}
	cf := mustRead(t, filepath.Join(dir, "compose.yml"))
	if !strings.Contains(cf, "mem_limit: 1024m") || strings.Count(cf, "mem_limit: 512m") != 1 {
		t.Errorf("db not raised alone:\n%s", cf)
	}
	if v := limitsOfSite(t, m, id)["db"]; v.Current != 1024 || v.Standard != 512 {
		t.Errorf("db %+v", v)
	}
	if !strings.Contains(calls(f), "up -d --wait") || strings.Contains(calls(f), "--force-recreate") {
		t.Errorf("memory alone needs no forced recreate: %s", calls(f))
	}
	// PHP memory_limit 512 MB: every container running PHP is raised with it, so it has room
	php := PHPStandard
	php.Memory, php.Upload = 512, 256
	if err := m.SetLimits(context.Background(), id, LimitChange{PHP: &php}); err != nil {
		t.Fatal(err)
	}
	ini := mustRead(t, filepath.Join(dir, "php.ini"))
	if !strings.Contains(ini, "memory_limit = 512M") || !strings.Contains(ini, "upload_max_filesize = 256M") || !strings.Contains(ini, "post_max_size = 256M") {
		t.Errorf("php.ini %s", ini)
	}
	lim := limitsOfSite(t, m, id)
	if lim["web"].Current != 768 || lim["db"].Current != 1024 {
		t.Errorf("PHP containers not raised with memory_limit: %+v", lim)
	}
	if !strings.Contains(calls(f), "--force-recreate") {
		t.Error("php.ini change did not recreate the containers")
	}
	// back to the standard
	if err := m.SetLimits(context.Background(), id, LimitChange{RAM: map[string]int{"db": 512, "web": 512}, PHP: &PHPStandard}); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Get(id)
	if s.RAM != nil || s.PHPLimits != nil || mustRead(t, filepath.Join(dir, "php.ini")) != PHPIni {
		t.Errorf("not back to the standard: %+v %+v", s.RAM, s.PHPLimits)
	}
	if strings.Contains(mustRead(t, filepath.Join(dir, "compose.yml")), "mem_limit: 1024m") {
		t.Error("compose still raised")
	}
}

func TestSetLimitsRefused(t *testing.T) {
	m, _, id := readySite(t)
	bad := PHPStandard
	bad.Memory = 200
	for name, c := range map[string]LimitChange{
		"below the standard": {RAM: map[string]int{"db": 256}},
		"not a choice":       {RAM: map[string]int{"db": 900}},
		"unknown service":    {RAM: map[string]int{"redis": 1024}},
		"PHP below standard": {PHP: &bad},
	} {
		if err := m.SetLimits(context.Background(), id, c); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, _ := m.Get(id); s.RAM != nil || s.PHPLimits != nil {
		t.Errorf("a refused change was saved: %+v", s)
	}
	// a static website has no PHP settings
	ms, _, st, _ := appSite(t, spec.Create{App: "static", Domain: "voltix.id", Primary: "domain", Server: "nginx", Title: "Voltix"})
	if err := ms.SetLimits(context.Background(), st.ID, LimitChange{PHP: &PHPStandard}); !errors.Is(err, ErrBadInput) {
		t.Errorf("static PHP: %v", err)
	}
	if l, _ := ms.Limits(st.ID); l.PHP != nil {
		t.Errorf("static shows PHP settings: %+v", l.PHP)
	}
}

func TestSetLimitsPutsBackOnFailure(t *testing.T) {
	m, f, id := readySite(t)
	dir := m.dir(id)
	before := mustRead(t, filepath.Join(dir, "compose.yml"))
	first := true
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d") && first {
			first = false
			return []byte("out of memory"), errors.New("exit 1"), true
		}
		return nil, nil, false
	}
	php := PHPStandard
	php.Memory = 768
	err := m.SetLimits(context.Background(), id, LimitChange{PHP: &php})
	if err == nil || !strings.Contains(err.Error(), "previous ones again") {
		t.Fatalf("err %v", err)
	}
	if mustRead(t, filepath.Join(dir, "compose.yml")) != before || mustRead(t, filepath.Join(dir, "php.ini")) != PHPIni {
		t.Error("previous files not put back")
	}
	if s, _ := m.Get(id); s.PHPLimits != nil || s.RAM != nil {
		t.Errorf("site.json not put back: %+v", s)
	}
}

func TestNginxFollowsUpload(t *testing.T) {
	c := req()
	c.Server = "nginx"
	m, _ := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	job, s, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("create %+v", v)
	}
	php := PHPStandard
	php.Upload = 1024
	if err := m.SetLimits(context.Background(), s.ID, LimitChange{PHP: &php}); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "nginx.conf"))
	if !strings.Contains(string(conf), "client_max_body_size 1024m;") || strings.Contains(string(conf), "128m") {
		t.Errorf("nginx.conf %s", conf)
	}
	if lim := limitsOfSite(t, m, s.ID); lim["php"].Current != 512 || !lim["php"].PHP || lim["web"].PHP {
		t.Errorf("upload alone raised memory or nginx counted as PHP: %+v", lim)
	}
}
