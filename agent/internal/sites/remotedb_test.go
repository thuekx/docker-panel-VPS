package sites

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeMaria greets like MariaDB, then echoes everything after the visitor's first packet.
func fakeMaria(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte{5, 0, 0, 0, 10, 'h', 'e', 'l', 'o'})
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func dialFrom(t *testing.T, src, dst string) net.Conn {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 2 * time.Second}
	c, err := d.Dial("tcp", dst)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}

func TestParseAllow(t *testing.T) {
	for _, bad := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/7", "example.org", "1.2.3.4/33"} {
		if _, _, err := parseAllow([]string{bad}); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s accepted", bad)
		}
	}
	out, _, err := parseAllow([]string{" 203.0.113.7 ", "198.51.100.9/24", "2001:db8::1", "203.0.113.7", ""})
	if err != nil || strings.Join(out, " ") != "203.0.113.7/32 198.51.100.0/24 2001:db8::1/128" {
		t.Fatalf("%v %v", out, err)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = net.IPv4(203, 0, 113, byte(i+1)).String()
	}
	if _, _, err := parseAllow(many); !errors.Is(err, ErrBadInput) {
		t.Fatal("21 addresses accepted")
	}
}

func TestRemoteDBGate(t *testing.T) {
	m, _, id := readySite(t)
	fw := &fakeHost{}
	m.UseFirewall(fw)
	m.remoteAddr = "127.0.0.1"
	up := fakeMaria(t)
	m.dbTargetFor = func(string) (string, error) { return up, nil }
	ctx := context.Background()
	if v, err := m.RemoteDB(id); err != nil || v.On || v.Engine != "mariadb" || v.User != "wordpress" {
		t.Fatalf("off %+v %v", v, err)
	}
	if _, err := m.SetRemoteDB(ctx, id, nil); !errors.Is(err, ErrBadInput) {
		t.Fatalf("off twice: %v", err)
	}
	j, err := m.SetRemoteDB(ctx, id, []string{"127.0.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, j); v.State != "done" {
		t.Fatalf("job %+v", v)
	}
	v, _ := m.RemoteDB(id)
	if !v.On || !v.Open || v.Port < mariaRemoteMin || v.Port >= mariaRemoteMin+remotePortSpan || strings.Join(v.Allow, ",") != "127.0.0.2/32" {
		t.Fatalf("on %+v", v)
	}
	want := "ufw allow from 127.0.0.2/32 to any port " + itoa(v.Port) + " proto tcp comment nostek db " + id
	if strings.Join(fw.calls, "|") != want {
		t.Fatalf("firewall %q", fw.calls)
	}
	addr := net.JoinHostPort("127.0.0.1", itoa(v.Port))

	// an address not on the list: closed before any byte
	c := dialFrom(t, "127.0.0.1", addr)
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("a refused visitor read %d bytes", n)
	}
	c.Close()
	// allowed but plain: gets the greeting, then is closed after its plain answer
	c = dialFrom(t, "127.0.0.2", addr)
	g := make([]byte, 9)
	if _, err := io.ReadFull(c, g); err != nil || string(g[4:]) != "\nhelo" {
		t.Fatalf("greeting %q %v", g, err)
	}
	c.Write([]byte{4, 0, 0, 1, 0x05, 0x02, 0, 0}) // capabilities without CLIENT_SSL
	if n, _ := c.Read(make([]byte, 8)); n != 0 {
		t.Fatal("a plain visitor got through")
	}
	c.Close()
	// allowed and asking for TLS: joined
	c = dialFrom(t, "127.0.0.2", addr)
	io.ReadFull(c, g)
	c.Write([]byte{4, 0, 0, 1, 0x05, 0x0a, 0, 0}) // CLIENT_SSL set
	c.Write([]byte("tls-bytes"))
	got := make([]byte, 8+9)
	if _, err := io.ReadFull(c, got); err != nil || !strings.HasSuffix(string(got), "tls-bytes") {
		t.Fatalf("joined %q %v", got, err)
	}
	c.Close()
	if v, _ = m.RemoteDB(id); v.Refused != 1 || v.Plain != 1 {
		t.Fatalf("counts %+v", v)
	}

	// a new list: one rule added, one dropped, same port, the gate follows at once
	fw.calls = nil
	j, _ = m.SetRemoteDB(ctx, id, []string{"127.0.0.1"})
	waitJob(t, m, j)
	if v2, _ := m.RemoteDB(id); v2.Port != v.Port {
		t.Fatal("port changed")
	}
	if len(fw.calls) != 2 || !strings.HasPrefix(fw.calls[0], "ufw delete allow from 127.0.0.2/32") || !strings.HasPrefix(fw.calls[1], "ufw allow from 127.0.0.1/32") {
		t.Fatalf("firewall %q", fw.calls)
	}
	c = dialFrom(t, "127.0.0.1", addr)
	if _, err := io.ReadFull(c, g); err != nil {
		t.Fatal("the new address is not let in")
	}
	c.Close()

	// delete to the trash closes it, restore opens it again
	if _, err := m.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("port open for a website in the trash")
	}
	tr, _ := m.Trash()
	if _, err := m.Restore(ctx, tr[0].Entry); err != nil && !strings.Contains(err.Error(), "restored") {
		t.Fatal(err)
	}
	if v, _ = m.RemoteDB(id); !v.Open {
		t.Fatal("not open after the restore")
	}
	// off
	j, _ = m.SetRemoteDB(ctx, id, nil)
	if st := waitJob(t, m, j); st.State != "done" {
		t.Fatalf("off %+v", st)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("port still open")
	}
	if v, _ = m.RemoteDB(id); v.On || v.Open {
		t.Fatalf("still on %+v", v)
	}
	if s, _ := m.Get(id); s.Remote != nil {
		t.Fatal("site.json keeps the access")
	}
}

func TestRemoteDBPostgresTLS(t *testing.T) {
	s := Site{ID: "x"}
	if strings.Contains(s.pgTLSCommand(), "ssl") {
		t.Fatal("TLS while off")
	}
	s.Remote = &RemoteDB{Port: pgRemoteMin, Allow: []string{"203.0.113.7/32"}}
	if !strings.Contains(s.pgTLSCommand(), "ssl=on") || !strings.Contains(s.pgTLSVolume(), "./db-tls:/tls:ro") {
		t.Fatal("no TLS while on")
	}
	// the gate: SSLRequest passes, a plain StartupMessage does not
	a, b := net.Pipe()
	go a.Write([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f})
	u1, u2 := net.Pipe()
	go io.Copy(io.Discard, u2)
	if err := pgNeedTLS(b, u1); err != nil {
		t.Fatalf("SSLRequest refused: %v", err)
	}
	a, b = net.Pipe()
	go a.Write([]byte{0, 0, 0, 40, 0, 3, 0, 0})
	if err := pgNeedTLS(b, u1); err == nil {
		t.Fatal("a plain startup passed")
	}
}

