package sites

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestPHPChange(t *testing.T) {
	m, f, id, _ := olderSite(t)
	f.dumped = "-- dump\n"
	st, err := m.PHPStatus(id)
	if err != nil || st.Current != "8.4" || st.Previous != "" || len(st.Choices) != len(catalog.All()) {
		t.Fatalf("status %+v %v", st, err)
	}
	for _, bad := range []string{"8.4", "7.4", ""} {
		if _, err := m.StartPHPChange(id, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	f.calls = nil
	job, err := m.StartPHPChange(id, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" || v.Kind != "php" || !strings.HasPrefix(v.Steps[len(v.Steps)-1], "done before-php-") {
		t.Fatalf("job %+v", v)
	}
	p83, _ := catalog.PHP("8.3")
	cf := mustRead(t, m.composePath(id))
	// the web server follows PHP; the database keeps its (older) image: newer ones only through "Update image"
	if !strings.Contains(cf, "image: "+p83.Apache+"\n") || !strings.Contains(cf, "image: "+oldDB+"\n") {
		t.Errorf("compose:\n%s", cf)
	}
	all := calls(f)
	if pull, up := strings.Index(all, "pull --quiet "+p83.Apache), strings.LastIndex(all, "up -d --wait"); pull < 0 || up < pull || strings.Contains(all, "pull --quiet "+catalog.MariaDB) {
		t.Errorf("pull/up:\n%s", all)
	}
	s, _ := m.Get(id)
	if s.PHP != "8.3" || s.PrevPHP != "8.4" {
		t.Errorf("site %+v", s)
	}
	if b, _ := m.ListBackups(id); len(b.Backups) != 1 || b.Backups[0].Kind != "before-php" {
		t.Errorf("backups %+v", b.Backups)
	}
	if st, _ := m.PHPStatus(id); st.Current != "8.3" || st.Previous != "8.4" {
		t.Errorf("status after %+v", st)
	}
	// the way back is another change
	ticking(m, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	job, _ = m.StartPHPChange(id, "8.4")
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("back %+v", v)
	}
	if s, _ := m.Get(id); s.PHP != "8.4" || s.PrevPHP != "8.3" {
		t.Errorf("after back %+v", s)
	}
}

func TestPHPChangeFailures(t *testing.T) {
	// a failed pull changes nothing, not even a backup
	m, f, id, old := olderSite(t)
	f.fail = "pull"
	job, _ := m.StartPHPChange(id, "8.5")
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "nothing was changed") {
		t.Fatalf("job %+v", v)
	}
	if b, _ := m.ListBackups(id); len(b.Backups) != 0 || mustRead(t, m.composePath(id)) != old {
		t.Error("a failed pull changed the site")
	}
	// the new PHP does not come up: previous files back, started again
	m, f, id, old = olderSite(t)
	f.dumped = "-- dump\n"
	ups := 0
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d --wait") {
			ups++
			if ups == 1 {
				return []byte("web is unhealthy"), errors.New("exit 1"), true
			}
		}
		return nil, nil, false
	}
	job, _ = m.StartPHPChange(id, "8.5")
	v := waitJob(t, m, job)
	if v.State != "failed" || !strings.Contains(v.Error, "runs on PHP 8.4 again") || ups != 2 {
		t.Fatalf("job %+v ups=%d", v, ups)
	}
	if s, _ := m.Get(id); mustRead(t, m.composePath(id)) != old || s.PHP != "8.4" || s.PrevPHP != "" {
		t.Errorf("not put back: %+v", s)
	}
	// neither starts: the backup to restore is named
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d --wait") {
			return []byte("broken"), errors.New("exit 1"), true
		}
		return nil, nil, false
	}
	ticking(m, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	job, _ = m.StartPHPChange(id, "8.5")
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "restore the backup before-php-") {
		t.Fatalf("job %+v", v)
	}
}

func TestPHPChangeApps(t *testing.T) {
	// Laravel: its minimum PHP is refused, the image is built again for the new version
	m, f, s, v := laravelSite(t, "")
	if v.State != "done" {
		t.Fatalf("create %+v", v)
	}
	st, _ := m.PHPStatus(s.ID)
	for _, c := range st.Choices {
		if want := spec.PHPLess(c.Version, "8.3"); (c.Why != "") != want {
			t.Errorf("choice %+v", c)
		}
	}
	if _, err := m.StartPHPChange(s.ID, "8.2"); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "needs PHP 8.3") {
		t.Errorf("below the minimum: %v", err)
	}
	f.calls = nil
	job, err := m.StartPHPChange(s.ID, "8.5")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	p85, _ := catalog.PHP("8.5")
	if mustRead(t, m.recipePath(s.ID)) != catalog.LaravelDockerfile(p85) || !strings.Contains(calls(f), "build --quiet php") {
		t.Errorf("recipe/build:\n%s", calls(f))
	}
	// a static website has no PHP
	ms, _, st2, _ := appSite(t, spec.Create{App: "static", Domain: "voltix.id", Primary: "domain", Server: "nginx", Title: "x"})
	if _, err := ms.PHPStatus(st2.ID); !errors.Is(err, ErrBadInput) {
		t.Errorf("static status: %v", err)
	}
	if _, err := ms.StartPHPChange(st2.ID, "8.4"); !errors.Is(err, ErrBadInput) {
		t.Errorf("static change: %v", err)
	}
}

func TestPHPChangeNative(t *testing.T) {
	m, f, h, p, s := nativeReady(t)
	f.dumped = "-- dump\n"
	if _, err := m.StartPHPChange(s.ID, "8.5"); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("not installed: %v", err)
	}
	job, err := m.StartPHPChange(s.ID, "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	if !strings.Contains(mustRead(t, p.unit(s.ID)), "php-fpm8.3") || !strings.Contains(h.all(), "systemctl restart "+NativeUnit(s.ID)) {
		t.Errorf("unit:\n%s", mustRead(t, p.unit(s.ID)))
	}
	if got, _ := m.Get(s.ID); got.PHP != "8.3" || got.PrevPHP != "8.4" {
		t.Errorf("site %+v", got)
	}
	h.fail = "systemctl restart"
	ticking(m, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	job, _ = m.StartPHPChange(s.ID, "8.4")
	if v := waitJob(t, m, job); v.State != "failed" || !strings.Contains(v.Error, "PHP 8.3") {
		t.Fatalf("failure %+v", v)
	}
	h.fail = ""
	if got, _ := m.Get(s.ID); got.PHP != "8.3" || !strings.Contains(mustRead(t, p.unit(s.ID)), "php-fpm8.3") {
		t.Errorf("not put back %+v", got)
	}
}
