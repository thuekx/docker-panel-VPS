package sites

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const ufwOut = `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 20022/tcp                  ALLOW IN    Anywhere
[ 2] 80/tcp                     ALLOW IN    Anywhere
[ 3] 443                        ALLOW IN    Anywhere
[ 4] 33061/tcp                  ALLOW IN    203.0.113.9                # nostek db voltix-id
[ 5] 8080/tcp                   ALLOW IN    Anywhere                   # nostek panel
[ 6] 9000:9100/udp              DENY IN     198.51.100.0/24
[ 7] 20022/tcp (v6)             ALLOW IN    Anywhere (v6)
[ 8] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)              # nostek panel
`

const f2bStatus = "Status\n|- Number of jail:\t3\n`- Jail list:\tnostek-panel, nostek-sftp, sshd\n"

type fwHost struct {
	mu    sync.Mutex
	calls []string
	ufw   string
	fail  string
}

func (h *fwHost) Host(_ context.Context, _ io.Reader, bin string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := hostBins[bin]; !ok {
		return nil, errors.New("not allowed")
	}
	line := bin + " " + strings.Join(args, " ")
	h.calls = append(h.calls, line)
	if h.fail != "" && strings.Contains(line, h.fail) {
		return []byte("ERROR: boom\nmore"), errors.New("exit 1")
	}
	switch {
	case line == "sshd -T":
		return []byte("port 20022\nport 2200\naddressfamily any\n"), nil
	case line == "ufw status numbered":
		return []byte(h.ufw), nil
	case line == "fail2ban-client status":
		return []byte(f2bStatus), nil
	case line == "fail2ban-client status sshd":
		return []byte("Status for the jail: sshd\n|- Filter\n|  |- Currently failed:\t2\n|  |- Total failed:\t40\n`- Actions\n   |- Currently banned:\t2\n   |- Total banned:\t7\n   `- Banned IP list:\t192.0.2.7 2001:db8::5 not-an-ip\n"), nil
	case strings.HasPrefix(line, "fail2ban-client status "):
		return []byte("Status\n|- Filter\n|  |- Currently failed:\t0\n`- Actions\n   |- Total banned:\t0\n   `- Banned IP list:\t\n"), nil
	}
	return nil, nil
}

func fwEnv(t *testing.T) (*Manager, *fwHost) {
	t.Helper()
	m, _ := newTest(t, nil)
	m.cfg.PanelUID = os.Getuid()
	etc := t.TempDir()
	m.UseServerEtc(etc)
	os.MkdirAll(filepath.Join(etc, "ufw"), 0o755)
	os.WriteFile(filepath.Join(etc, "ufw", "user.rules"), []byte("v4 rules before\n"), 0o640)
	os.WriteFile(filepath.Join(etc, "ufw", "user6.rules"), []byte("v6 rules before\n"), 0o640)
	os.MkdirAll(filepath.Join(etc, "fail2ban", "jail.d"), 0o755)
	h := &fwHost{ufw: ufwOut}
	m.UseFirewall(h)
	return m, h
}

func TestServerSecurityView(t *testing.T) {
	m, _ := fwEnv(t)
	v := m.ServerSecurityView(context.Background())
	if !v.Active || len(v.Rules) != 8 || v.FWError != "" || v.F2BError != "" {
		t.Fatalf("view %+v", v)
	}
	if strings.Join(intsS(v.SSHPorts), ",") != "2200,20022" {
		t.Fatalf("ssh ports %v", v.SSHPorts)
	}
	want := []struct {
		to, from, locked, comment string
		v6                        bool
	}{
		{"20022/tcp", "Anywhere", "ssh", "", false}, {"80/tcp", "Anywhere", "web", "", false}, {"443", "Anywhere", "web", "", false},
		{"33061/tcp", "203.0.113.9", "panel", "nostek db voltix-id", false}, {"8080/tcp", "Anywhere", "", "nostek panel", false},
		{"9000:9100/udp", "198.51.100.0/24", "", "", false}, {"20022/tcp", "Anywhere", "ssh", "", true}, {"8080/tcp", "Anywhere", "", "nostek panel", true},
	}
	for i, w := range want {
		r := v.Rules[i]
		if r.N != i+1 || r.To != w.to || r.From != w.from || r.Locked != w.locked || r.Comment != w.comment || r.V6 != w.v6 {
			t.Errorf("rule %d: %+v, want %+v", i+1, r, w)
		}
	}
	if len(v.Jails) != 3 || v.Jails[2].Name != "sshd" || strings.Join(v.Jails[2].Banned, ",") != "192.0.2.7,2001:db8::5" || v.Jails[2].Failed != 2 || v.Jails[2].Total != 7 {
		t.Fatalf("jails %+v", v.Jails)
	}
}

func intsS(a []int) []string {
	var s []string
	for _, x := range a {
		s = append(s, itoa(x))
	}
	return s
}

func TestFirewallChangeRefusals(t *testing.T) {
	m, h := fwEnv(t)
	ctx := context.Background()
	for name, c := range map[string]FWChange{
		"bad port":        {Op: "allow", Port: "0", Proto: "tcp"},
		"big port":        {Op: "allow", Port: "70000", Proto: "tcp"},
		"wide range":      {Op: "allow", Port: "1:2000", Proto: "tcp"},
		"reversed range":  {Op: "allow", Port: "200:100", Proto: "tcp"},
		"range both":      {Op: "allow", Port: "8000:8010", Proto: "both"},
		"bad proto":       {Op: "allow", Port: "8080", Proto: "icmp"},
		"bad from":        {Op: "allow", Port: "8080", Proto: "tcp", From: "evil; rm -rf /"},
		"text port":       {Op: "allow", Port: "8080 && reboot", Proto: "tcp"},
		"deny ssh":        {Op: "deny", Port: "20022", Proto: "tcp"},
		"deny ssh range":  {Op: "deny", Port: "2000:2300", Proto: "tcp"},
		"delete ssh":      {Op: "delete", Rule: 1, Match: "20022/tcp|ALLOW|Anywhere"},
		"delete web":      {Op: "delete", Rule: 2, Match: "80/tcp|ALLOW|Anywhere"},
		"delete db":       {Op: "delete", Rule: 4, Match: "33061/tcp|ALLOW|203.0.113.9"},
		"renumbered list": {Op: "delete", Rule: 5, Match: "9000:9100/udp|DENY|198.51.100.0/24"},
		"unknown op":      {Op: "reset"},
	} {
		if _, err := m.FirewallChange(ctx, c); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, c := range h.calls {
		if strings.HasPrefix(c, "ufw ") && c != "ufw status numbered" {
			t.Fatalf("a refused change ran %q", c)
		}
	}
}

func TestFirewallTrial(t *testing.T) {
	m, h := fwEnv(t)
	ctx := context.Background()
	now := time.Unix(1764200000, 0)
	m.now = func() time.Time { return now }
	tr, err := m.FirewallChange(ctx, FWChange{Op: "allow", Port: "8443", Proto: "tcp", From: "203.0.113.0/24 "})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Deadline-tr.Started != int64(fwTrialFor/time.Second) || tr.What != "allow 8443 tcp from 203.0.113.0/24" {
		t.Fatalf("trial %+v", tr)
	}
	if last := h.calls[len(h.calls)-1]; last != "ufw allow from 203.0.113.0/24 to any port 8443 proto tcp comment nostek panel" {
		t.Fatalf("ufw call %q", last)
	}
	if _, err := m.FirewallChange(ctx, FWChange{Op: "allow", Port: "9443", Proto: "tcp"}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a second change during a trial: %v", err)
	}
	if v := m.ServerSecurityView(ctx); v.Trial == nil || v.Trial.What != tr.What {
		t.Fatalf("trial not shown: %+v", v.Trial)
	}
	// ufw changed its files; undo puts the kept ones back and reloads
	etc := m.ufwEtc()
	os.WriteFile(filepath.Join(etc, "user.rules"), []byte("v4 rules after\n"), 0o640)
	undone := ""
	m.onFWUndo = func(why string, err error) { undone = why }
	if err := m.FirewallUndo(ctx, "asked"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(etc, "user.rules")); string(b) != "v4 rules before\n" || undone != "asked" || h.calls[len(h.calls)-1] != "ufw reload" {
		t.Fatalf("undo: %q %q %v", b, undone, h.calls)
	}
	if m.fwTrial() != nil {
		t.Fatal("trial left after undo")
	}
	// confirm keeps the change
	if _, err := m.FirewallChange(ctx, FWChange{Op: "allow", Port: "8443", Proto: "both"}); err != nil {
		t.Fatal(err)
	}
	if last := h.calls[len(h.calls)-1]; last != "ufw allow 8443 comment nostek panel" {
		t.Fatalf("ufw call %q", last)
	}
	os.WriteFile(filepath.Join(etc, "user.rules"), []byte("v4 rules after\n"), 0o640)
	if err := m.FirewallConfirm(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(etc, "user.rules")); string(b) != "v4 rules after\n" || m.fwTrial() != nil {
		t.Fatal("confirm did not keep the change")
	}
	if err := m.FirewallConfirm(); !errors.Is(err, ErrBadInput) {
		t.Fatalf("confirm without a trial: %v", err)
	}
	// delete with the listed rule
	if _, err := m.FirewallChange(ctx, FWChange{Op: "delete", Rule: 6, Match: "9000:9100/udp|DENY|198.51.100.0/24"}); err != nil {
		t.Fatal(err)
	}
	if last := h.calls[len(h.calls)-1]; last != "ufw --force delete 6" {
		t.Fatalf("ufw call %q", last)
	}
	m.FirewallConfirm()
	// a change ufw refuses leaves no trial and the files as they were
	h.fail = "ufw deny"
	if _, err := m.FirewallChange(ctx, FWChange{Op: "deny", Port: "3306", Proto: "tcp"}); err == nil || m.fwTrial() != nil {
		t.Fatalf("refused by ufw: %v", err)
	}
}

func TestFirewallTrialTimesOut(t *testing.T) {
	m, h := fwEnv(t)
	ctx := context.Background()
	if _, err := m.FirewallChange(ctx, FWChange{Op: "allow", Port: "8443", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	m.onFWUndo = func(why string, err error) { done <- why }
	m.fwSchedule(20 * time.Millisecond)
	select {
	case why := <-done:
		if !strings.Contains(why, "not confirmed in time") {
			t.Fatalf("why %q", why)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the trial was not undone")
	}
	if m.fwTrial() != nil || h.calls[len(h.calls)-1] != "ufw reload" {
		t.Fatal("trial left")
	}
	// a trial past its deadline is undone at agent start
	if _, err := m.FirewallChange(ctx, FWChange{Op: "allow", Port: "8443", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	m.ResumeFirewallTrial(ctx)
	if m.fwTrial() != nil {
		t.Fatal("an old trial survived the restart")
	}
}

func TestFail2ban(t *testing.T) {
	m, h := fwEnv(t)
	ctx := context.Background()
	if err := m.Unban(ctx, "sshd", "192.0.2.7"); err != nil || h.calls[len(h.calls)-1] != "fail2ban-client set sshd unbanip 192.0.2.7" {
		t.Fatalf("unban %v %v", err, h.calls)
	}
	for _, x := range [][2]string{{"sshd;x", "192.0.2.7"}, {"sshd", "192.0.2.7 --x"}, {"", "192.0.2.7"}} {
		if err := m.Unban(ctx, x[0], x[1]); !errors.Is(err, ErrBadInput) {
			t.Errorf("%v: %v", x, err)
		}
	}
	got, err := m.SetFail2banAllow(ctx, []string{" 203.0.113.9 ", "198.51.100.7/24", "203.0.113.9", ""})
	if err != nil || strings.Join(got, ",") != "203.0.113.9,198.51.100.0/24" {
		t.Fatalf("allow %v %v", got, err)
	}
	if b, _ := os.ReadFile(m.f2bAllowFile()); !strings.Contains(string(b), "ignoreip = 127.0.0.1/8 ::1 203.0.113.9 198.51.100.0/24\n") {
		t.Fatalf("file %s", b)
	}
	if strings.Join(m.f2bAllow(), ",") != "203.0.113.9,198.51.100.0/24" || h.calls[len(h.calls)-1] != "fail2ban-client reload" {
		t.Fatal("allow-list not read back or not reloaded")
	}
	for _, bad := range [][]string{{"0.0.0.0/0"}, {"10.0.0.0/7"}, {"::/8"}, {"nope"}, make([]string, fwMaxAllow+1)} {
		if bad[0] == "" {
			for i := range bad {
				bad[i] = "192.0.2." + itoa(i%250+1)
			}
		}
		if _, err := m.SetFail2banAllow(ctx, bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("%v: %v", bad[0], err)
		}
	}
}

func TestSyncFail2ban(t *testing.T) {
	m, h := fwEnv(t)
	ctx := context.Background()
	if err := m.SyncFail2ban(ctx); err != nil {
		t.Fatal(err)
	}
	jail, _ := os.ReadFile(filepath.Join(m.f2bEtc(), "jail.d", "nostek-panel.local"))
	for _, want := range []string{"[nostek-panel]", "[nostek-sftp]", "banaction = iptables-allports[chain=DOCKER-USER]", "backend = auto",
		"logpath = " + filepath.Join(m.panelDir(), authFailFile), "logpath = " + filepath.Join(m.cfg.Data, "sftp-state", authFailFile)} {
		if !strings.Contains(string(jail), want) {
			t.Errorf("jail lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(m.panelDir(), authFailFile)); err != nil {
		t.Fatal("the panel's log is not there for fail2ban")
	}
	n := len(h.calls)
	if h.calls[n-1] != "fail2ban-client reload" {
		t.Fatalf("calls %v", h.calls)
	}
	m.SyncFail2ban(ctx)
	if len(h.calls) != n {
		t.Fatal("reloaded without a change")
	}
	// the filter matches the lines the panel and SFTP write
	f, _ := os.ReadFile(filepath.Join(m.f2bEtc(), "filter.d", "nostek-auth.conf"))
	if !strings.Contains(string(f), `failregex = ^\S+ nostek-auth fail ip=<HOST> service=\S+\s*$`) {
		t.Fatalf("filter %s", f)
	}
}
