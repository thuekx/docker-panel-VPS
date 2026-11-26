package api

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type apiHost struct{ calls []string }

func (h *apiHost) Host(_ context.Context, _ io.Reader, bin string, args ...string) ([]byte, error) {
	line := bin + " " + strings.Join(args, " ")
	h.calls = append(h.calls, line)
	if line == "ufw status numbered" {
		return []byte("Status: active\n[ 1] 22/tcp                     ALLOW IN    Anywhere\n"), nil
	}
	return nil, nil
}

func TestServerSecurityRoutes(t *testing.T) {
	c, logPath, m := serveM(t, map[int]bool{os.Getuid(): true})
	etc := t.TempDir()
	os.MkdirAll(filepath.Join(etc, "ufw"), 0o755)
	os.WriteFile(filepath.Join(etc, "ufw", "user.rules"), []byte("a"), 0o640)
	os.WriteFile(filepath.Join(etc, "ufw", "user6.rules"), []byte("b"), 0o640)
	m.UseServerEtc(etc)
	h := &apiHost{}
	m.UseFirewall(h)
	if code, out := do(t, c, "GET", "/v1/server-security", ""); code != 200 || out["active"] != true {
		t.Fatalf("view %d %v", code, out)
	}
	for _, body := range []string{`{"op":"deny","port":"22","proto":"tcp"}`, `{"op":"allow","port":"x","proto":"tcp"}`, `{"op":"allow","x":1}`} {
		if code, out := do(t, c, "POST", "/v1/firewall", body); code != 400 {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	if code, out := do(t, c, "POST", "/v1/firewall", `{"op":"allow","port":"8443","proto":"tcp"}`); code != 200 || out["what"] != "allow 8443 tcp" {
		t.Fatalf("change %d %v", code, out)
	}
	if code, _ := do(t, c, "POST", "/v1/firewall/confirm", ""); code != 200 {
		t.Fatalf("confirm %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/firewall/undo", ""); code != 400 {
		t.Fatalf("undo without a trial %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/fail2ban/unban", `{"jail":"sshd","ip":"192.0.2.7"}`); code != 200 {
		t.Fatalf("unban %d", code)
	}
	if code, out := do(t, c, "POST", "/v1/fail2ban/allow", `{"allow":["203.0.113.9"]}`); code != 200 {
		t.Fatalf("allow %d %v", code, out)
	}
	b, _ := os.ReadFile(logPath)
	for _, want := range []string{"trial: allow 8443 tcp", "firewall-confirm", "sshd 192.0.2.7", "fail2ban-allow"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}
