package sftpd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/thuekx/docker-panel-VPS/panel/internal/authlog"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func hash(t *testing.T, pw string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func put(t *testing.T, dir string, s Site) {
	t.Helper()
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, s.Site+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	srv     *Server
	authLog string
	addr    string
	root    string // the website folder
	cfg     string
	log     *logBuf
	key     ssh.Signer
	close   func()
}

func start(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{root: filepath.Join(base, "sites", "a-test", "wp"), cfg: filepath.Join(base, "config"), log: &logBuf{}}
	for _, d := range []string{e.root, filepath.Join(e.root, "wp-content", "uploads"), e.cfg, filepath.Join(base, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(base, "outside", "secret.txt"), []byte("host secret"), 0o644)
	_ = os.WriteFile(filepath.Join(e.root, "index.php"), []byte("<?php echo 1;"), 0o644)
	_ = os.Symlink(filepath.Join(base, "outside"), filepath.Join(e.root, "escape"))
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	e.key, _ = ssh.NewSignerFromKey(priv)
	pub := string(ssh.MarshalAuthorizedKey(e.key.PublicKey()))
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 33, 33
	}
	put(t, e.cfg, Site{Site: "a-test", Root: e.root, UID: uid, GID: gid, Users: []User{
		{Name: "ana", Hash: hash(t, "pass-ana-1"), Dir: "/"},
		{Name: "upl", Hash: hash(t, "pass-upl-1"), Keys: []string{pub}, Dir: "/wp-content/uploads"},
	}})
	e.srv = New(e.cfg, e.log)
	e.authLog = filepath.Join(base, "auth-fail.log")
	e.srv.AuthFail = authlog.Open(e.authLog, "sftp")
	if err := e.srv.Load(); err != nil {
		t.Fatal(err)
	}
	hk, err := HostKey(filepath.Join(base, "host_key"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.addr = l.Addr().String()
	go e.srv.Serve(l, hk)
	e.close = func() { l.Close() }
	t.Cleanup(e.close)
	return e
}

func (e *env) dial(user string, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	return ssh.Dial("tcp", e.addr, &ssh.ClientConfig{User: user, Auth: auth, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
}

func (e *env) sftp(t *testing.T, user string, auth ssh.AuthMethod) *sftp.Client {
	t.Helper()
	c, err := e.dial(user, auth)
	if err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
	t.Cleanup(func() { c.Close() })
	s, err := sftp.NewClient(c)
	if err != nil {
		t.Fatalf("sftp %s: %v", user, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSFTP(t *testing.T) {
	e := start(t)
	c := e.sftp(t, "ana", ssh.Password("pass-ana-1"))
	if wd, _ := c.Getwd(); wd != "/" {
		t.Errorf("start folder %q", wd)
	}
	f, err := c.Create("/wp-content/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(bytes.Repeat([]byte("x"), 100000))
	f.Close()
	if fi, err := os.Stat(filepath.Join(e.root, "wp-content", "new.txt")); err != nil || fi.Size() != 100000 || fi.Mode().Perm() != 0o644 {
		t.Fatalf("uploaded file: %v %v", fi, err)
	}
	if os.Getuid() == 0 {
		for _, n := range []string{"wp-content/new.txt"} {
			fi, _ := os.Stat(filepath.Join(e.root, n))
			if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 33 || st.Gid != 33 {
				t.Errorf("%s not owned by the file user", n)
			}
		}
	}
	g, _ := c.Open("/index.php")
	b, _ := io.ReadAll(g)
	g.Close()
	if string(b) != "<?php echo 1;" {
		t.Errorf("download %q", b)
	}
	if err := c.Mkdir("/newdir"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(e.root, "newdir")); os.Getuid() == 0 && fi.Sys().(*syscall.Stat_t).Uid != 33 {
		t.Error("new folder not owned by the file user")
	}
	if err := c.Rename("/wp-content/new.txt", "/newdir/moved.txt"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename("/newdir/moved.txt", "/index.php"); err == nil {
		t.Error("plain rename replaced a file")
	}
	if err := c.PosixRename("/newdir/moved.txt", "/newdir/m2.txt"); err != nil {
		t.Error(err)
	}
	if err := c.Chmod("/newdir/m2.txt", 0o4755); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(e.root, "newdir", "m2.txt")); fi.Mode()&os.ModeSetuid != 0 || fi.Mode().Perm() != 0o755 {
		t.Errorf("chmod kept %v", fi.Mode())
	}
	if err := c.Truncate("/newdir/m2.txt", 10); err != nil {
		t.Error(err)
	}
	if err := c.Chtimes("/newdir/m2.txt", time.Unix(1700000000, 0), time.Unix(1700000000, 0)); err != nil {
		t.Error(err)
	}
	ls, err := c.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range ls {
		names = append(names, x.Name())
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "index.php") || !strings.Contains(got, "newdir") {
		t.Errorf("listing %s", got)
	}
	// no way out
	for _, p := range []string{"/../outside/secret.txt", "/escape/secret.txt", "../../outside/secret.txt"} {
		if h, err := c.Open(p); err == nil {
			b, _ := io.ReadAll(h)
			h.Close()
			if strings.Contains(string(b), "host secret") {
				t.Errorf("%s read outside the website", p)
			}
		}
	}
	if _, err := c.Create("/escape/planted.txt"); err == nil {
		t.Error("wrote through a link that leaves the website")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(e.root)), "..", "outside", "planted.txt")); err == nil {
		t.Error("planted outside")
	}
	if err := c.Symlink("/etc/passwd", "/pw"); err == nil {
		t.Error("made a link")
	}
	if err := c.Link("/index.php", "/hard"); err == nil {
		t.Error("made a hard link")
	}
	if rp, _ := c.RealPath("../../.."); rp != "/" {
		t.Errorf("realpath %q", rp)
	}
	if err := c.Remove("/newdir"); err == nil {
		t.Error("Remove took a folder")
	}
	if err := c.Remove("/newdir/m2.txt"); err != nil {
		t.Error(err)
	}
	if err := c.RemoveDirectory("/newdir"); err != nil {
		t.Error(err)
	}
	if err := c.RemoveDirectory("/"); err == nil {
		t.Error("removed the website folder")
	}
	if !strings.Contains(e.log.String(), `"event":"login"`) || strings.Contains(e.log.String(), "pass-ana-1") {
		t.Errorf("log: %s", e.log.String())
	}
}

func TestSubfolderKeyAndShell(t *testing.T) {
	e := start(t)
	c := e.sftp(t, "upl", ssh.PublicKeys(e.key))
	if _, err := c.Stat("/index.php"); err == nil {
		t.Error("an uploads account sees the website root")
	}
	w, err := c.Create("/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("jpg"))
	w.Close()
	if _, err := os.Stat(filepath.Join(e.root, "wp-content", "uploads", "photo.jpg")); err != nil {
		t.Error("file not in the uploads folder")
	}
	// another key is refused; a shell or a command is refused
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	os2, _ := ssh.NewSignerFromKey(other)
	if _, err := e.dial("upl", ssh.PublicKeys(os2)); err == nil {
		t.Error("another key logged in")
	}
	cl, err := e.dial("ana", ssh.Password("pass-ana-1"))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	s, _ := cl.NewSession()
	if out, err := s.CombinedOutput("id"); err == nil || strings.Contains(string(out), "uid=") {
		t.Errorf("exec ran: %q %v", out, err)
	}
	s2, _ := cl.NewSession()
	if err := s2.Shell(); err == nil {
		t.Error("shell accepted")
	}
	if _, err := cl.Dial("tcp", "127.0.0.1:22"); err == nil {
		t.Error("port forwarding accepted")
	}
}

func TestLoginsAndReload(t *testing.T) {
	e := start(t)
	if _, err := e.dial("ana", ssh.Password("wrong")); err == nil {
		t.Fatal("wrong password accepted")
	}
	if b, _ := os.ReadFile(e.authLog); !strings.Contains(string(b), " nostek-auth fail ip=127.0.0.1 service=sftp\n") {
		t.Fatalf("fail2ban log: %q", b)
	}
	if _, err := e.dial("nobody", ssh.Password("x")); err == nil {
		t.Fatal("unknown account accepted")
	}
	if _, err := e.dial("upl", ssh.Password("pass-ana-1")); err == nil {
		t.Fatal("another account's password accepted")
	}
	// a second website using the same name: that name works for neither
	put(t, e.cfg, Site{Site: "b-test", Root: e.root, UID: 1000, GID: 1000, Users: []User{{Name: "ana", Hash: hash(t, "pass-b"), Dir: "/"}}})
	if err := e.srv.Load(); err == nil || !strings.Contains(err.Error(), "two websites") {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := e.dial("ana", ssh.Password("pass-ana-1")); err == nil {
		t.Error("a name used twice logged in")
	}
	os.Remove(filepath.Join(e.cfg, "b-test.json"))
	// a broken file keeps the previous accounts
	time.Sleep(10 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(e.cfg, "a-test.json"), []byte(`{"site":"a-test","root":"relative","uid":1,"gid":1}`), 0o600)
	if err := e.srv.Load(); err == nil {
		t.Error("broken file accepted")
	}
	if strings.Join(e.srv.Accounts(), ",") != "ana,upl" {
		t.Errorf("accounts after a broken file: %v", e.srv.Accounts())
	}
	os.Remove(filepath.Join(e.cfg, "a-test.json"))
	_ = e.srv.Load()
	if len(e.srv.Accounts()) != 0 {
		t.Error("removed accounts kept")
	}
}

func TestBan(t *testing.T) {
	e := start(t)
	now := time.Unix(1700000000, 0)
	var mu sync.Mutex
	real := false
	e.srv.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if real {
			return time.Now()
		}
		return now
	}
	for i := 0; i < FailLimit; i++ {
		e.srv.failed("192.0.2.1")
	}
	if !e.srv.banned("192.0.2.1") || e.srv.banned("192.0.2.2") {
		t.Fatal("ban")
	}
	mu.Lock()
	now = now.Add(BanTime + time.Second)
	mu.Unlock()
	if e.srv.banned("192.0.2.1") {
		t.Error("ban did not end")
	}
	// failures spread past the window do not add up
	for i := 0; i < FailLimit*2; i++ {
		e.srv.failed("192.0.2.3")
		mu.Lock()
		now = now.Add(FailWindow / time.Duration(FailLimit-2))
		mu.Unlock()
	}
	if e.srv.banned("192.0.2.3") {
		t.Error("slow failures banned")
	}
	// a real address gets banned after FailLimit wrong passwords, then even the right one is refused
	mu.Lock()
	real = true
	mu.Unlock()
	for i := 0; i < FailLimit; i++ {
		_, _ = e.dial("ana", ssh.Password("wrong"))
	}
	if _, err := e.dial("ana", ssh.Password("pass-ana-1")); err == nil {
		t.Error("banned address logged in")
	}
	if !strings.Contains(e.log.String(), `"event":"ban"`) {
		t.Errorf("no ban line: %s", e.log.String())
	}
}

func TestCheck(t *testing.T) {
	good := Site{Site: "x-test", Root: "/sites/x-test/wp", UID: 33, GID: 33, Users: []User{{Name: "budi", Hash: hash(t, "p"), Dir: "/"}}}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	u := good.Users[0]
	for _, bad := range []Site{
		{Site: "X", Root: good.Root, UID: 33, GID: 33},
		{Site: "x-test", Root: "/", UID: 33, GID: 33},
		{Site: "x-test", Root: good.Root, UID: 0, GID: 0},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{{Name: "Root!", Hash: u.Hash, Dir: "/"}}},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{u, u}},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{{Name: "budi", Dir: "/"}}},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{{Name: "budi", Hash: "plain", Dir: "/"}}},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{{Name: "budi", Hash: u.Hash, Dir: "/a/../b"}}},
		{Site: "x-test", Root: good.Root, UID: 33, GID: 33, Users: []User{{Name: "budi", Keys: []string{"ssh-dss AAAA x"}, Dir: "/"}}},
	} {
		if bad.Check() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestChangedAccountEndsSession(t *testing.T) {
	e := start(t)
	c := e.sftp(t, "ana", ssh.Password("pass-ana-1"))
	if _, err := c.Stat("/index.php"); err != nil {
		t.Fatal(err)
	}
	// the folder of ana changes: her open connection ends, the other account keeps working
	other := e.sftp(t, "upl", ssh.PublicKeys(e.key))
	b, _ := os.ReadFile(filepath.Join(e.cfg, "a-test.json"))
	var st Site
	_ = json.Unmarshal(b, &st)
	st.Users[0].Dir = "/wp-content"
	time.Sleep(10 * time.Millisecond)
	put(t, e.cfg, st)
	if err := e.srv.Load(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := c.Stat("/index.php"); err == nil {
		t.Error("the session of a changed account still works")
	}
	if _, err := other.Stat("/"); err != nil {
		t.Errorf("an unchanged account lost its session: %v", err)
	}
	c2 := e.sftp(t, "ana", ssh.Password("pass-ana-1"))
	if _, err := c2.Stat("/uploads"); err != nil {
		t.Errorf("new login does not see the new folder: %v", err)
	}
}
