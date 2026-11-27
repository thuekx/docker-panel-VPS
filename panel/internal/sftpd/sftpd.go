// Package sftpd is the SFTP service of the core stack (SFTP-01): one SSH server for every website, file transfer
// only. Each account is locked to its website's folder (or a folder inside it) through os.Root, so no path,
// symbolic link or ".." reaches anything else. There is no shell, no command, no port forwarding. Files and folders
// it creates belong to the website's file user, like the ones the File Manager writes.
//
// The agent writes one file per website in the config folder (<data>/sftp/<id>.json, 0600): the folder, the file
// user and the accounts with a bcrypt hash and/or public keys. The service reloads a file when it changes.
package sftpd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/thuekx/docker-panel-VPS/panel/internal/authlog"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

// Limits of the service.
const (
	MaxConns      = 64               // connections at the same time, all addresses
	MaxConnsPerIP = 8                // connections at the same time from one address
	FailLimit     = 10               // failed logins from one address within FailWindow before it is refused
	FailWindow    = 10 * time.Minute //
	BanTime       = 30 * time.Minute // how long an address is refused after FailLimit
	IdleTimeout   = 15 * time.Minute // a connection with no traffic is closed
	authTimeout   = 30 * time.Second // the time to log in
)

// User is one SFTP account of a website.
type User struct {
	Name string   `json:"name"`
	Hash string   `json:"hash,omitempty"` // bcrypt of the password, empty for key-only accounts
	Keys []string `json:"keys,omitempty"` // public keys, authorized_keys format
	Dir  string   `json:"dir"`            // the folder the account sees as "/", inside the website ("/" = all of it)
}

// Site is one website's file in the config folder.
type Site struct {
	Site  string `json:"site"`
	Root  string `json:"root"` // the website's files as the service sees them (/sites/<id>/wp)
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
	Users []User `json:"users"`
}

var (
	siteRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// NameRe is the form of an account name: unique on the server, so it also says which website.
	NameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)
)

// CheckDir checks the folder of an account: "/" or an absolute path without "..", spaces are allowed.
func CheckDir(d string) error {
	if d == "" || !strings.HasPrefix(d, "/") || len(d) > 512 || strings.ContainsAny(d, "\x00\\\n\r") || path.Clean(d) != d {
		return fmt.Errorf("folder %q must be an absolute path like /wp-content (no .., no trailing /)", d)
	}
	return nil
}

// Check refuses a file that the service must not use.
func (s Site) Check() error {
	if !siteRe.MatchString(s.Site) {
		return fmt.Errorf("site %q is not a website id", s.Site)
	}
	if !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || s.Root == "/" {
		return fmt.Errorf("root %q must be an absolute folder", s.Root)
	}
	if s.UID <= 0 || s.GID <= 0 {
		return errors.New("uid and gid must be set (never root)")
	}
	seen := map[string]bool{}
	for _, u := range s.Users {
		if !NameRe.MatchString(u.Name) || seen[u.Name] {
			return fmt.Errorf("account %q is not valid or listed twice", u.Name)
		}
		seen[u.Name] = true
		if err := CheckDir(u.Dir); err != nil {
			return err
		}
		if u.Hash == "" && len(u.Keys) == 0 {
			return fmt.Errorf("account %q has neither a password nor a key", u.Name)
		}
		if u.Hash != "" {
			if _, err := bcrypt.Cost([]byte(u.Hash)); err != nil {
				return fmt.Errorf("account %q: the password is not a bcrypt hash", u.Name)
			}
		}
		for _, k := range u.Keys {
			if _, err := ParseKey(k); err != nil {
				return fmt.Errorf("account %q: %v", u.Name, err)
			}
		}
	}
	return nil
}

// ParseKey reads one public key line and refuses weak ones (RSA under 2048 bits, DSA).
func ParseKey(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, errors.New("not a public key (one line like ssh-ed25519 AAAA… name)")
	}
	switch k.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		if ck, ok := k.(ssh.CryptoPublicKey); ok {
			if rk, ok := ck.CryptoPublicKey().(interface{ Size() int }); ok && rk.Size()*8 < 2048 {
				return nil, errors.New("RSA keys need at least 2048 bits")
			}
		}
	default:
		return nil, fmt.Errorf("key type %s is not accepted (use ed25519, ECDSA or RSA)", k.Type())
	}
	return k, nil
}

// account is what a login needs, built from the config files.
type account struct {
	site     string
	root     string // the account's own folder on disk
	uid, gid int
	hash     []byte
	keys     [][]byte // marshalled public keys
}

// active is what a connection logged in with, to see whether its account changed.
type active struct {
	user  string
	stamp string
}

// stamp sums up an account: a change of folder, password or keys ends its open connections.
func (a account) stamp() string {
	return fmt.Sprintf("%s|%s|%d|%d|%s|%x", a.site, a.root, a.uid, a.gid, a.hash, a.keys)
}

// Server is the SFTP service.
type Server struct {
	// AuthFail gets one line per failed password for fail2ban (SRV-01); nil writes nothing
	AuthFail *authlog.Log

	dir    string
	logger *log.Logger
	now    func() time.Time

	mu       sync.RWMutex
	accounts map[string]account
	stamps   map[string]string // file -> modification time and size, to reload only changed files
	files    map[string]Site   // last good content per file

	amu    sync.Mutex
	active map[*ssh.ServerConn]active // logged-in connections, closed when their account goes or changes

	cmu   sync.Mutex
	conns map[string]int // open connections per address
	total int
	fails map[string][]time.Time
	bans  map[string]time.Time
}

// New makes a service reading its accounts from dir and writing its log lines to w.
func New(dir string, w io.Writer) *Server {
	return &Server{dir: dir, logger: log.New(w, "", 0), now: time.Now, accounts: map[string]account{}, stamps: map[string]string{},
		files: map[string]Site{}, active: map[*ssh.ServerConn]active{}, conns: map[string]int{}, fails: map[string][]time.Time{}, bans: map[string]time.Time{}}
}

// dummyHash keeps a login for an unknown name as slow as one for a known name.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("nostek-sftp-no-such-user"), bcrypt.DefaultCost)

// Load reads the config folder again. A file that changed and is broken keeps its previous content; the error
// names it. An account name used by two websites belongs to neither until one is renamed.
func (s *Server) Load() error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []string
	present := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		present[name] = true
		fi, err := e.Info()
		if err != nil {
			continue
		}
		stamp := fmt.Sprint(fi.ModTime().UnixNano(), fi.Size())
		if s.stamps[name] == stamp {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		var st Site
		if err == nil {
			err = json.Unmarshal(b, &st)
		}
		if err == nil {
			err = st.Check()
		}
		if err == nil && st.Site+".json" != name {
			err = errors.New("file name and site differ")
		}
		if err != nil {
			errs = append(errs, name+": "+err.Error())
			continue
		}
		s.stamps[name], s.files[name] = stamp, st
	}
	for name := range s.files {
		if !present[name] {
			delete(s.files, name)
			delete(s.stamps, name)
		}
	}
	acc := map[string]account{}
	dup := map[string]bool{}
	for _, st := range s.files {
		for _, u := range st.Users {
			if _, ok := acc[u.Name]; ok {
				dup[u.Name] = true
				continue
			}
			a := account{site: st.Site, root: filepath.Join(st.Root, filepath.FromSlash(strings.TrimPrefix(u.Dir, "/"))), uid: st.UID, gid: st.GID}
			if u.Hash != "" {
				a.hash = []byte(u.Hash)
			}
			for _, k := range u.Keys {
				if pk, err := ParseKey(k); err == nil {
					a.keys = append(a.keys, pk.Marshal())
				}
			}
			acc[u.Name] = a
		}
	}
	for n := range dup {
		delete(acc, n)
		errs = append(errs, "account "+n+" is used by two websites")
	}
	s.accounts = acc
	s.amu.Lock()
	for c, a := range s.active {
		if cur, ok := acc[a.user]; !ok || cur.stamp() != a.stamp {
			_ = c.Close()
			delete(s.active, c)
		}
	}
	s.amu.Unlock()
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Accounts lists the account names in use, sorted.
func (s *Server) Accounts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for n := range s.accounts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Watch reloads the config folder every interval until stop is closed.
func (s *Server) Watch(stop <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := s.Load(); err != nil {
				s.logger.Printf(`{"t":%d,"event":"config","error":%q}`, s.now().Unix(), err.Error())
			}
		}
	}
}

func (s *Server) event(kind, ip, user, site, detail string) {
	b, _ := json.Marshal(map[string]any{"t": s.now().Unix(), "event": kind, "ip": ip, "user": user, "site": site, "detail": detail})
	s.logger.Print(string(b))
}

// banned is true while an address is refused; it also forgets old failures.
func (s *Server) banned(ip string) bool {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	until, ok := s.bans[ip]
	if ok && s.now().Before(until) {
		return true
	}
	delete(s.bans, ip)
	return false
}

// failed counts a failed login; at FailLimit within FailWindow the address is refused for BanTime.
func (s *Server) failed(ip string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	now := s.now()
	var keep []time.Time
	for _, t := range s.fails[ip] {
		if now.Sub(t) < FailWindow {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	s.fails[ip] = keep
	if len(keep) >= FailLimit {
		s.bans[ip] = now.Add(BanTime)
		delete(s.fails, ip)
		go s.event("ban", ip, "", "", fmt.Sprintf("%d failed logins, refused for %s", len(keep), BanTime))
	}
	if len(s.fails) > 10000 { // a flood of addresses: forget the oldest state rather than grow without end
		s.fails = map[string][]time.Time{}
	}
}

func (s *Server) take(ip string) bool {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if s.total >= MaxConns || s.conns[ip] >= MaxConnsPerIP {
		return false
	}
	s.total++
	s.conns[ip]++
	return true
}

func (s *Server) give(ip string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.total--
	if s.conns[ip]--; s.conns[ip] <= 0 {
		delete(s.conns, ip)
	}
}

func (s *Server) lookup(name string) (account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[name]
	return a, ok
}

// Config is the SSH side: password and key logins against the accounts, nothing else.
func (s *Server) Config(hostKey ssh.Signer) *ssh.ServerConfig {
	c := &ssh.ServerConfig{
		MaxAuthTries:  6,
		ServerVersion: "SSH-2.0-NostekSFTP",
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			ip := hostOf(m.RemoteAddr())
			a, ok := s.lookup(m.User())
			h := dummyHash
			if ok && a.hash != nil {
				h = a.hash
			}
			if bcrypt.CompareHashAndPassword(h, pw) != nil || !ok || a.hash == nil {
				s.failed(ip)
				s.AuthFail.Fail(ip)
				s.event("login-failed", ip, m.User(), a.site, "password")
				return nil, errors.New("wrong name or password")
			}
			return &ssh.Permissions{Extensions: map[string]string{"site": a.site}}, nil
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			a, ok := s.lookup(m.User())
			if ok {
				kb := key.Marshal()
				for _, k := range a.keys {
					if bytes.Equal(k, kb) {
						return &ssh.Permissions{Extensions: map[string]string{"site": a.site, "key": ssh.FingerprintSHA256(key)}}, nil
					}
				}
			}
			return nil, errors.New("key not accepted")
		},
	}
	c.AddHostKey(hostKey)
	return c
}

func hostOf(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

// idleConn closes a connection that has no traffic for IdleTimeout.
type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(IdleTimeout))
	return c.Conn.Read(p)
}

func (c idleConn) Write(p []byte) (int, error) {
	_ = c.SetDeadline(time.Now().Add(IdleTimeout))
	return c.Conn.Write(p)
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(l net.Listener, hostKey ssh.Signer) error {
	cfg := s.Config(hostKey)
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go s.handle(c, cfg)
	}
}

func (s *Server) handle(c net.Conn, cfg *ssh.ServerConfig) {
	ip := hostOf(c.RemoteAddr())
	if s.banned(ip) || !s.take(ip) {
		_ = c.Close()
		return
	}
	defer s.give(ip)
	defer c.Close()
	login := time.AfterFunc(authTimeout, func() { _ = c.Close() }) // the time to log in
	sc, chans, reqs, err := ssh.NewServerConn(idleConn{c}, cfg)
	login.Stop()
	if err != nil {
		return
	}
	defer sc.Close()
	user, site := sc.User(), sc.Permissions.Extensions["site"]
	a, ok := s.lookup(user)
	if !ok {
		return
	}
	s.amu.Lock()
	s.active[sc] = active{user: user, stamp: a.stamp()}
	s.amu.Unlock()
	defer func() { s.amu.Lock(); delete(s.active, sc); s.amu.Unlock() }()
	how := "password"
	if k := sc.Permissions.Extensions["key"]; k != "" {
		how = "key " + k
	}
	s.event("login", ip, user, site, how)
	go ssh.DiscardRequests(reqs) // no port forwarding, no keep-alive replies needed
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.Prohibited, "only SFTP is offered")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, creqs, user, ip)
	}
}

// session waits for the "sftp" subsystem and refuses everything else (shell, exec, pty, env).
func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request, user, ip string) {
	defer ch.Close()
	for r := range reqs {
		var p struct{ Name string }
		if r.Type != "subsystem" || ssh.Unmarshal(r.Payload, &p) != nil || p.Name != "sftp" {
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
			if r.Type == "shell" || r.Type == "exec" {
				_, _ = io.WriteString(ch.Stderr(), "This account is for SFTP only.\r\n")
				return
			}
			continue
		}
		a, ok := s.lookup(user) // taken again: the account may have been removed since the login
		if !ok {
			_ = r.Reply(false, nil)
			return
		}
		root, err := os.OpenRoot(a.root)
		if err != nil {
			_ = r.Reply(false, nil)
			_, _ = io.WriteString(ch.Stderr(), "The folder of this account does not exist.\r\n")
			return
		}
		_ = r.Reply(true, nil)
		go ssh.DiscardRequests(reqs)
		fsys := &jail{root: root, uid: a.uid, gid: a.gid}
		srv := sftp.NewRequestServer(ch, sftp.Handlers{FileGet: fsys, FilePut: fsys, FileCmd: fsys, FileList: fsys})
		err = srv.Serve()
		_ = srv.Close()
		_ = root.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			s.event("session-error", ip, user, a.site, err.Error())
		}
		return
	}
}

// HostKey reads the service's ed25519 host key from file, making it on first start (0600).
func HostKey(file string) (ssh.Signer, error) {
	if b, err := os.ReadFile(file); err == nil {
		return ssh.ParsePrivateKey(b)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "nostek-sftp host key")
	if err != nil {
		return nil, err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(blk), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, file); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
