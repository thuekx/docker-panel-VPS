package sites

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpCompose writes rendered files when NP_DUMP is set (used by the harness to run `docker compose config`).
func TestDumpCompose(t *testing.T) {
	out := os.Getenv("NP_DUMP")
	if out == "" {
		t.Skip("NP_DUMP not set")
	}
	for _, srv := range []string{"apache", "nginx"} {
		s := Site{ID: "t-" + srv, Server: srv, PHP: "8.4", Hosts: []Host{
			{Name: "example.test", Role: "primary", Routed: true}, {Name: "www.example.test", Role: "www", Routed: true},
			{Name: "t.preview.example.test", Role: "preview", Routed: true}}}
		cf, err := s.Compose()
		if err != nil {
			t.Fatal(err)
		}
		d := filepath.Join(out, srv)
		os.MkdirAll(filepath.Join(d, "secrets"), 0o700)
		os.WriteFile(filepath.Join(d, "compose.yml"), []byte(cf), 0o644)
		os.WriteFile(filepath.Join(d, "secrets", "db_password"), []byte("x"), 0o444)
		os.WriteFile(filepath.Join(d, "php.ini"), []byte(PHPIni), 0o644)
		os.WriteFile(filepath.Join(d, "nginx.conf"), []byte(NginxConf), 0o644)
	}
}
