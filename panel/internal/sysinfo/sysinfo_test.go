package sysinfo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMissingSourcesStayUnknown(t *testing.T) {
	d := t.TempDir()
	s := Reader{ProcDir: filepath.Join(d, "none"), BackupFile: filepath.Join(d, "none")}.Read(context.Background())
	for name, st := range map[string]Status{"mem": s.MemStatus, "load": s.LoadStatus, "docker": s.DockerStatus, "backup": s.BackupStatus} {
		if st != Unknown {
			t.Errorf("%s = %s, want unknown", name, st)
		}
	}
}

func TestMemAndBackupLevels(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "meminfo"), "MemTotal: 1024000 kB\nMemAvailable: 51200 kB\n")
	bf := filepath.Join(d, "last")
	r := Reader{ProcDir: d, BackupFile: bf}

	write(t, bf, fmt.Sprintf("result=PASS unix=%d\n", time.Now().Add(-2*time.Hour).Unix()))
	s := r.Read(context.Background())
	if s.MemStatus != Bad {
		t.Errorf("mem 95%% used = %s, want bad", s.MemStatus)
	}
	if s.BackupStatus != OK {
		t.Errorf("fresh backup = %s, want ok", s.BackupStatus)
	}
	write(t, bf, fmt.Sprintf("result=PASS unix=%d\n", time.Now().Add(-40*time.Hour).Unix()))
	if st := r.Read(context.Background()).BackupStatus; st != Warn {
		t.Errorf("stale backup = %s, want warn", st)
	}
	write(t, bf, fmt.Sprintf("result=FAIL unix=%d\n", time.Now().Unix()))
	if st := r.Read(context.Background()).BackupStatus; st != Bad {
		t.Errorf("failed backup = %s, want bad", st)
	}
	write(t, bf, "garbage\n")
	if st := r.Read(context.Background()).BackupStatus; st != Unknown {
		t.Errorf("unreadable backup = %s, want unknown", st)
	}
}

func TestDockerStatus(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ServerVersion":"28.0.0","Containers":3,"ContainersRunning":2}`)
	}))
	defer ok.Close()
	s := Reader{DockerURL: ok.URL}.Read(context.Background())
	if s.DockerStatus != OK || s.ContainersRunning != 2 {
		t.Errorf("docker = %s running=%d", s.DockerStatus, s.ContainersRunning)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	if st := (Reader{DockerURL: down.URL}).Read(context.Background()).DockerStatus; st != Bad {
		t.Errorf("docker 503 = %s, want bad", st)
	}
}
