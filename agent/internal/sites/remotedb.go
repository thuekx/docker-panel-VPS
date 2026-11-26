package sites

// Database access from outside (DB-02, DB-2 = a port per website with an address list). Off by default. The agent
// itself listens on the website's port: a visitor whose address is not on the list is closed before a byte is
// read, an allowed one is joined to the db container on the website's own network. Docker publishes nothing, so
// nothing is open while the agent is down or the list is empty. ufw gets one rule per address as a second filter.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RemoteDB is a website's database access from outside (nil in site.json = off).
type RemoteDB struct {
	Port  int      `json:"port"`
	Allow []string `json:"allow"`
}

const (
	maxRemoteAllow = 20
	mariaRemoteMin = 33061
	pgRemoteMin    = 54321
	remotePortSpan = 1000
	pgUID          = 999 // the postgres user of the official image
)

// RemoteView is what the panel shows.
type RemoteView struct {
	On      bool     `json:"on"`
	Engine  string   `json:"engine"` // mariadb | postgres
	Host    string   `json:"host"`
	Port    int      `json:"port,omitempty"`
	Allow   []string `json:"allow"`
	User    string   `json:"user"`
	DB      string   `json:"db"`
	TLS     string   `json:"tls"`
	Refused int64    `json:"refused"` // visitors closed since the agent started
	Open    bool     `json:"open"`    // the agent listens now
	Plain   int64    `json:"plain"`   // allowed visitors closed for not asking for TLS
}

// parseAllow checks an address list: addresses or networks, IPv4 or IPv6, never everything.
func parseAllow(list []string) ([]string, []netip.Prefix, error) {
	var out []string
	var ps []netip.Prefix
	seen := map[string]bool{}
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(a, "/") {
			x, err := netip.ParsePrefix(a)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %q is not an address or network", ErrBadInput, a)
			}
			p = x.Masked()
		} else {
			ip, err := netip.ParseAddr(a)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %q is not an address or network", ErrBadInput, a)
			}
			p = netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen())
		}
		if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
			return nil, nil, fmt.Errorf("%w: %s is too wide; list your own addresses", ErrBadInput, p)
		}
		if seen[p.String()] {
			continue
		}
		seen[p.String()] = true
		out, ps = append(out, p.String()), append(ps, p)
	}
	if len(out) > maxRemoteAllow {
		return nil, nil, fmt.Errorf("%w: at most %d addresses", ErrBadInput, maxRemoteAllow)
	}
	return out, ps, nil
}

// gate is one listening port.
type gate struct {
	ln      net.Listener
	site    string
	inner   int // 3306 or 5432
	allow   atomic.Pointer[[]netip.Prefix]
	refused atomic.Int64
	plain   atomic.Int64 // allowed visitors closed because they did not ask for TLS
	m       *Manager
}

// pgNeedTLS lets a PostgreSQL visitor in only when its first message asks for TLS (SSLRequest, or direct TLS).
func pgNeedTLS(c, up net.Conn) error {
	b := make([]byte, 8)
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return err
	}
	if b[0] == 0x16 { // a TLS ClientHello (sslnegotiation=direct, PostgreSQL 17 and newer)
		_, err := up.Write(b[:1])
		return err
	}
	if _, err := io.ReadFull(c, b[1:]); err != nil {
		return err
	}
	if !(b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 8 && b[4] == 0x04 && b[5] == 0xd2 && b[6] == 0x16 && b[7] == 0x2f) {
		return errors.New("no TLS asked")
	}
	_, err := up.Write(b)
	return err
}

// mariaNeedTLS passes the server's greeting and lets a MariaDB visitor in only when its answer is an SSL request
// (capability CLIENT_SSL); a plain login would send the password hash and the data in clear.
func mariaNeedTLS(c, up net.Conn) error {
	pkt := func(r io.Reader) ([]byte, error) {
		h := make([]byte, 4)
		if _, err := io.ReadFull(r, h); err != nil {
			return nil, err
		}
		n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
		if n > 1<<16 {
			return nil, errors.New("packet too large")
		}
		b := make([]byte, 4+n)
		copy(b, h)
		_, err := io.ReadFull(r, b[4:])
		return b, err
	}
	greet, err := pkt(up)
	if err != nil {
		return err
	}
	if _, err := c.Write(greet); err != nil {
		return err
	}
	ans, err := pkt(c)
	if err != nil {
		return err
	}
	if len(ans) < 8 || (uint32(ans[4])|uint32(ans[5])<<8)&0x0800 == 0 {
		return errors.New("no TLS asked")
	}
	_, err = up.Write(ans)
	return err
}

type gates struct {
	mu sync.Mutex
	by map[string]*gate // site id
}

func (g *gate) allowed(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip, ok := netip.AddrFromSlice(ta.IP)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	for _, p := range *g.allow.Load() {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (g *gate) serve() {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if !g.allowed(c.RemoteAddr()) {
			g.refused.Add(1)
			_ = c.Close()
			continue
		}
		go g.join(c)
	}
}

// join connects an allowed visitor to the db container (its address looked up each time: it changes on recreate).
func (g *gate) join(c net.Conn) {
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	addr, err := g.m.dbTarget(ctx, g.site, g.inner)
	cancel()
	if err != nil {
		return
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	up, err := d.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer up.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	_ = up.SetDeadline(time.Now().Add(15 * time.Second))
	if g.inner == 5432 {
		err = pgNeedTLS(c, up)
	} else {
		err = mariaNeedTLS(c, up)
	}
	if err != nil {
		g.plain.Add(1)
		return
	}
	_ = c.SetDeadline(time.Time{})
	_ = up.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if t, ok := dst.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}

// dbTarget is the db container's address and port on the website's network.
func (m *Manager) dbTarget(ctx context.Context, id string, port int) (string, error) {
	if m.dbTargetFor != nil {
		return m.dbTargetFor(id)
	}
	out, err := m.run.Docker(ctx, m.cfg.Data, nil, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", Project(id)+"-db-1")
	if err != nil {
		return "", errors.New("the database container is not running")
	}
	for _, f := range strings.Fields(string(out)) {
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return net.JoinHostPort(f, strconv.Itoa(port)), nil
		}
	}
	return "", errors.New("the database container has no address")
}

func (m *Manager) remoteListen(port int) (net.Listener, error) {
	addr := ":" + strconv.Itoa(port)
	if m.remoteAddr != "" {
		addr = net.JoinHostPort(m.remoteAddr, strconv.Itoa(port))
	}
	return net.Listen("tcp", addr)
}

// openGate starts or updates a website's gate; closeGate stops it.
func (m *Manager) openGate(s Site) error {
	_, ps, err := parseAllow(s.Remote.Allow)
	if err != nil {
		return err
	}
	m.gates.mu.Lock()
	defer m.gates.mu.Unlock()
	if m.gates.by == nil {
		m.gates.by = map[string]*gate{}
	}
	if g := m.gates.by[s.ID]; g != nil {
		if g.ln.Addr().(*net.TCPAddr).Port == s.Remote.Port {
			g.allow.Store(&ps)
			return nil
		}
		_ = g.ln.Close()
		delete(m.gates.by, s.ID)
	}
	ln, err := m.remoteListen(s.Remote.Port)
	if err != nil {
		return fmt.Errorf("port %d cannot be opened: %v", s.Remote.Port, err)
	}
	inner := 3306
	if s.IsPostgres() {
		inner = 5432
	}
	g := &gate{ln: ln, site: s.ID, inner: inner, m: m}
	g.allow.Store(&ps)
	m.gates.by[s.ID] = g
	go g.serve()
	return nil
}

func (m *Manager) closeGate(id string) {
	m.gates.mu.Lock()
	defer m.gates.mu.Unlock()
	if g := m.gates.by[id]; g != nil {
		_ = g.ln.Close()
		delete(m.gates.by, id)
	}
}

func (m *Manager) gateOf(id string) *gate {
	m.gates.mu.Lock()
	defer m.gates.mu.Unlock()
	return m.gates.by[id]
}

// remotePort picks the lowest free port of the engine's range that no other website uses.
func (m *Manager) remotePort(s Site) (int, error) {
	list, err := m.List()
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, x := range list {
		if x.Remote != nil && x.ID != s.ID {
			used[x.Remote.Port] = true
		}
	}
	base := mariaRemoteMin
	if s.IsPostgres() {
		base = pgRemoteMin
	}
	for p := base; p < base+remotePortSpan; p++ {
		if used[p] {
			continue
		}
		if ln, err := m.remoteListen(p); err == nil {
			_ = ln.Close()
			return p, nil
		}
	}
	return 0, errors.New("no free port for the database")
}

// ufw keeps one rule per address and port; the comment names the website so the rules can be found again.
func (m *Manager) firewall(ctx context.Context, id string, port int, add, drop []string) error {
	if m.fw == nil {
		return errors.New("the firewall tool is not available")
	}
	comment := "nostek db " + id
	for _, a := range drop {
		_, _ = m.fw.Host(ctx, nil, "ufw", "delete", "allow", "from", a, "to", "any", "port", strconv.Itoa(port), "proto", "tcp")
	}
	for _, a := range add {
		if out, err := m.fw.Host(ctx, nil, "ufw", "allow", "from", a, "to", "any", "port", strconv.Itoa(port), "proto", "tcp", "comment", comment); err != nil {
			return fmt.Errorf("the firewall refused %s: %s", a, tail(out))
		}
	}
	return nil
}

func (m *Manager) dbTLSDir(id string) string { return filepath.Join(m.dir(id), "db-tls") }

// pgTLS makes the self-signed certificate PostgreSQL serves to visitors from outside (kept while access stays on).
func (m *Manager) pgTLS(s Site) error {
	dir := m.dbTLSDir(s.ID)
	if _, err := os.Stat(filepath.Join(dir, "server.key")); err == nil {
		return nil
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	sn, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	now := m.now()
	tpl := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: m.cfg.PublicIPv4}, NotBefore: now.Add(-time.Hour),
		NotAfter: now.AddDate(10, 0, 0), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	if ip := net.ParseIP(m.cfg.PublicIPv4); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	uid := pgUID
	if os.Getuid() != 0 {
		uid = os.Getuid() // tests run without root
	}
	for name, v := range map[string][]byte{
		"server.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"server.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
	} {
		f := filepath.Join(dir, name)
		if err := writeFile(f, v, 0o600); err != nil {
			return err
		}
		if err := os.Chown(f, uid, uid); err != nil {
			return err
		}
	}
	return nil
}

// RemoteDB returns a website's access from outside.
func (m *Manager) RemoteDB(id string) (RemoteView, error) {
	s, err := m.Get(id)
	if err != nil {
		return RemoteView{}, err
	}
	if !s.HasDB() {
		return RemoteView{}, fmt.Errorf("%w: this website has no database", ErrBadInput)
	}
	v := RemoteView{Engine: "mariadb", Host: m.cfg.PublicIPv4, Allow: []string{}, User: s.DBName(), DB: s.DBName(),
		TLS: "MariaDB TLS (certificate made by the server)"}
	if s.IsPostgres() {
		v.Engine, v.TLS = "postgres", "sslmode=require (self-signed certificate)"
	}
	if s.Remote != nil {
		v.On, v.Port, v.Allow = true, s.Remote.Port, s.Remote.Allow
	}
	if g := m.gateOf(id); g != nil {
		v.Open, v.Refused, v.Plain = true, g.refused.Load(), g.plain.Load()
	}
	return v, nil
}

// SetRemoteDB turns access from outside on with an address list, changes the list, or turns it off (empty list).
// A job "remote-db": the website's settings (PostgreSQL restarts its database with TLS), the firewall, the gate.
func (m *Manager) SetRemoteDB(ctx context.Context, id string, allow []string) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !s.HasDB() {
		return "", fmt.Errorf("%w: this website has no database", ErrBadInput)
	}
	if s.IsNative() {
		return "", fmt.Errorf("%w: access from outside is for Docker websites only (the host MariaDB is shared)", ErrBadInput)
	}
	if s.IsMySQL() && len(allow) > 0 {
		return "", fmt.Errorf("%w: access from outside is not offered for the MySQL database of a ready app", ErrBadInput)
	}
	list, _, err := parseAllow(allow)
	if err != nil {
		return "", err
	}
	if len(list) == 0 && s.Remote == nil {
		return "", fmt.Errorf("%w: nothing to change", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("remote-db", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		prev := s.Remote
		next := s
		var old []string
		port := 0
		if prev != nil {
			old, port = prev.Allow, prev.Port
		}
		j.Step("settings")
		if len(list) == 0 {
			next.Remote = nil
		} else {
			if port == 0 {
				p, err := m.remotePort(s)
				if err != nil {
					j.Fail(err.Error())
					return
				}
				port = p
			}
			next.Remote = &RemoteDB{Port: port, Allow: list}
			if s.IsPostgres() {
				if err := m.pgTLS(s); err != nil {
					j.Fail("the database certificate could not be made: " + err.Error())
					return
				}
			}
		}
		if s.IsPostgres() && (prev == nil) != (next.Remote == nil) {
			// the database starts again with (or without) TLS for visitors from outside
			if err := m.writeLimits(ctx, next); err != nil {
				j.Fail(err.Error())
				return
			}
		} else if err := writeSite(m.dir(id), next); err != nil {
			j.Fail(err.Error())
			return
		}
		if next.Remote == nil {
			m.closeGate(id)
			j.Step("firewall")
			_ = m.firewall(ctx, id, port, nil, old)
			if s.IsPostgres() {
				_ = os.RemoveAll(m.dbTLSDir(id))
			}
			j.Done("off")
			return
		}
		j.Step("firewall")
		add, drop := diffList(old, list)
		if err := m.firewall(ctx, id, port, add, drop); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Step("gate")
		if err := m.openGate(next); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done(fmt.Sprintf("port %d", port))
	}()
	return j.ID, nil
}

func diffList(old, cur []string) (add, drop []string) {
	o, c := map[string]bool{}, map[string]bool{}
	for _, a := range old {
		o[a] = true
	}
	for _, a := range cur {
		c[a] = true
		if !o[a] {
			add = append(add, a)
		}
	}
	for _, a := range old {
		if !c[a] {
			drop = append(drop, a)
		}
	}
	return add, drop
}

// SyncRemoteDB puts back the firewall rules and opens the gates of every website with access from outside (agent
// start; after a server restore the next start does it). ufw skips a rule it already has.
func (m *Manager) SyncRemoteDB(ctx context.Context) error {
	list, err := m.List()
	if err != nil {
		return err
	}
	var errs []string
	for _, s := range list {
		if s.Remote == nil {
			m.closeGate(s.ID)
			continue
		}
		if err := m.firewall(ctx, s.ID, s.Remote.Port, s.Remote.Allow, nil); err != nil {
			errs = append(errs, s.ID+": "+err.Error())
			continue
		}
		if err := m.openGate(s); err != nil {
			errs = append(errs, s.ID+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// UseFirewall gives the manager the host's firewall tool (ufw).
func (m *Manager) UseFirewall(h HostRunner) { m.fw = h }

// remoteDown closes a website's database port and its firewall rules (the setting stays in site.json).
func (m *Manager) remoteDown(ctx context.Context, s Site) {
	if s.Remote == nil {
		return
	}
	m.closeGate(s.ID)
	_ = m.firewall(ctx, s.ID, s.Remote.Port, nil, s.Remote.Allow)
}

// remoteUp opens them again (trash restore).
func (m *Manager) remoteUp(ctx context.Context, s Site) {
	if s.Remote == nil {
		return
	}
	if m.firewall(ctx, s.ID, s.Remote.Port, s.Remote.Allow, nil) == nil {
		_ = m.openGate(s)
	}
}
