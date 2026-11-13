package sites

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestSSHKeyFormat(t *testing.T) {
	priv, pub, fp, err := newSSHKey("nostek-panel-deploy-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5") || !strings.HasPrefix(fp, "SHA256:") || !strings.Contains(string(priv), "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatalf("key %q %q", pub, fp)
	}
	kg, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("no ssh-keygen")
	}
	p := filepath.Join(t.TempDir(), "k")
	if err := os.WriteFile(p, priv, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(kg, "-y", "-f", p).Output()
	if f := strings.Fields(string(out)); err != nil || len(f) < 2 || !strings.HasPrefix(pub, f[0]+" "+f[1]+" ") {
		t.Fatalf("ssh-keygen reads %q (%v), want %q", out, err, pub)
	}
	if out, err := exec.Command(kg, "-l", "-f", p).Output(); err != nil || !strings.Contains(string(out), fp) {
		t.Fatalf("fingerprint %q (%v), want %s", out, err, fp)
	}
}

func TestPrivateRepoDeployKey(t *testing.T) {
	m, f := newTest(t, map[string][]string{"voltix.id": {ip}})
	m.cfg.FileUID = os.Getuid()
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "run" && strings.Contains(j, "np-build-"):
			out, err := okBuild(1)
			return out, err, true
		case strings.Contains(j, " up -d --wait") && strings.HasSuffix(j, " app"):
			return nil, nil, true
		}
		return nil, nil, false
	}
	c := spec.Create{App: "go", Domain: "voltix.id", Primary: "domain", Version: "1.27", Repo: "git@github.com:owner/private-app.git", Branch: "main"}
	if _, err := m.Plan(context.Background(), c); err == nil || !strings.Contains(err.Error(), "deploy key") {
		t.Fatalf("SSH repository without a key: %v", err)
	}
	c.KeyID = "0123456789abcdef"
	if _, err := m.Plan(context.Background(), c); !errors.Is(err, ErrBadInput) {
		t.Fatalf("unknown key: %v", err)
	}
	k, err := m.NewDeployKey()
	if err != nil {
		t.Fatal(err)
	}
	if spec.KeyID(k.ID) != nil || !strings.HasPrefix(k.Public, "ssh-ed25519 ") || k.Fingerprint == "" {
		t.Fatalf("key %+v", k)
	}
	if st, _ := os.Stat(filepath.Join(m.pendingKeyDir(), k.ID)); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("pending key mode %v", st)
	}
	if d, _ := os.Stat(m.pendingKeyDir()); d.Mode().Perm() != 0o700 {
		t.Errorf("pending key folder mode %v", d.Mode())
	}
	c.KeyID = k.ID
	job, s, err := m.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	if fileExists(filepath.Join(m.pendingKeyDir(), k.ID)) {
		t.Error("pending key not moved into the website")
	}
	for _, p := range []string{m.deployKeyPath(s.ID), m.deployKeyPath(s.ID) + ".pub", m.knownHostsPath(s.ID)} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v", p, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(m.dir(s.ID), "site.json")); strings.Contains(string(b), k.ID) {
		t.Error("key id stored in site.json")
	}
	var build string
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == "run" {
			build = strings.Join(call, " ")
		}
	}
	for _, want := range []string{m.deployKeyPath(s.ID) + ":" + buildKeyPath + ":ro", m.knownHostsPath(s.ID) + ":" + buildKnownHosts,
		"GIT_SSH_COMMAND=ssh -i " + buildKeyPath, "IdentitiesOnly=yes", "BatchMode=yes", "StrictHostKeyChecking=accept-new",
		"UserKnownHostsFile=" + buildKnownHosts, "NOSTEK_REPO=git@github.com:owner/private-app.git", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(build, want) {
			t.Errorf("build lacks %q", want)
		}
	}
	if strings.Contains(build, "PRIVATE KEY") {
		t.Error("private key passed on the command line")
	}
	if err := os.WriteFile(m.knownHostsPath(s.ID), []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := m.SiteDeployKey(s.ID)
	if err != nil || got.Public != k.Public || got.Fingerprint != k.Fingerprint || len(got.HostKeys) != 1 || !strings.HasPrefix(got.HostKeys[0], "ssh-ed25519 SHA256:") {
		t.Fatalf("site key %+v %v", got, err)
	}
	if _, err := m.SiteDeployKey("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing site: %v", err)
	}

	// a public repository has no key
	pm, pf, ps, _ := nodeSite(t, okBuild, nil)
	if _, err := pm.SiteDeployKey(ps.ID); !errors.Is(err, ErrNoDeployKey) {
		t.Errorf("public repository key: %v", err)
	}
	for _, call := range pf.calls {
		if j := strings.Join(call, " "); strings.Contains(j, "GIT_SSH_COMMAND") || strings.Contains(j, buildKeyPath) {
			t.Error("deploy key mounted for a public repository")
		}
	}

	// unused keys are removed after a day, and their number is bounded
	old, _ := m.NewDeployKey()
	m.now = func() time.Time { return time.Now().Add(PendingKeyTTL + time.Hour) }
	if gone, _ := m.Purge(); len(gone) != 1 || gone[0] != "deploy-key "+old.ID {
		t.Fatalf("purge %v", gone)
	}
	m.now = time.Now
	for i := 0; i < MaxPendingKeys; i++ {
		if _, err := m.NewDeployKey(); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	if _, err := m.NewDeployKey(); !errors.Is(err, ErrBadInput) {
		t.Errorf("unbounded pending keys: %v", err)
	}
}
