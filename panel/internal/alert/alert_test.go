package alert

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tok = "123456789:AAH-test-token-abcdefghijklmnopqrstuv"

func TestKeyAndSeal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "alerts.key")
	k, err := LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 || fi.Size() != 32 {
		t.Fatalf("key file %v", fi.Mode())
	}
	sealed, err := k.Seal("s3cret-pass")
	if err != nil || strings.Contains(sealed, "s3cret") {
		t.Fatal(sealed, err)
	}
	k2, _ := LoadKey(p) // the same key after a restart
	if got, err := k2.Open(sealed); err != nil || got != "s3cret-pass" {
		t.Fatal(got, err)
	}
	if _, err := NewKey(make([]byte, 32)).Open(sealed); err == nil {
		t.Error("another key opened it")
	}
	if s, _ := k.Seal(""); s != "" {
		t.Error("empty sealed")
	}
	_ = os.WriteFile(p, []byte("short"), 0o600)
	if _, err := LoadKey(p); err == nil {
		t.Error("short key accepted")
	}
}

func TestChecks(t *testing.T) {
	if CheckTelegram(Telegram{Token: tok, Chat: "123456789"}) != nil || CheckTelegram(Telegram{Token: tok, Chat: "-1001234567890"}) != nil || CheckTelegram(Telegram{Token: tok, Chat: "@nostek_alerts"}) != nil {
		t.Error("good telegram refused")
	}
	for _, bad := range []Telegram{{Token: "x", Chat: "1"}, {Token: tok, Chat: "abc"}, {Token: tok + "\n", Chat: "1"}, {Token: tok, Chat: "1; rm"}} {
		if CheckTelegram(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	good := Mail{Host: "smtp.example.com", Port: 587, User: "u", Pass: "p", From: "panel@example.com", To: []string{"a@example.com"}}
	if err := CheckMail(good); err != nil {
		t.Error(err)
	}
	for _, f := range []func(*Mail){
		func(m *Mail) { m.Host = "localhost" }, func(m *Mail) { m.Port = 25 }, func(m *Mail) { m.From = "Panel <p@example.com>" },
		func(m *Mail) { m.To = nil }, func(m *Mail) { m.To = []string{"x"} }, func(m *Mail) { m.User = "a\r\nRCPT TO:<x@y>" },
		func(m *Mail) { m.To = []string{"a@b.c", "a@b.c", "a@b.c", "a@b.c", "a@b.c", "a@b.c"} },
	} {
		m := good
		f(&m)
		if CheckMail(m) == nil {
			t.Errorf("accepted %+v", m)
		}
	}
}

func TestSendTelegram(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/bot"+tok+"/sendMessage" {
			w.WriteHeader(404)
			return
		}
		if r.PostForm.Get("chat_id") == "999" {
			w.WriteHeader(400)
			return
		}
		got = r.PostForm.Get("text")
	}))
	defer ts.Close()
	old := TelegramAPI
	TelegramAPI = ts.URL
	defer func() { TelegramAPI = old }()
	if err := SendTelegram(context.Background(), Telegram{Token: tok, Chat: "1"}, "halo <b>"); err != nil || got != "halo <b>" {
		t.Fatal(err, got)
	}
	err := SendTelegram(context.Background(), Telegram{Token: tok, Chat: "999"}, "x")
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Errorf("chat refused: %v", err)
	}
	err = SendTelegram(context.Background(), Telegram{Token: "1:" + strings.Repeat("b", 35), Chat: "1"}, "x")
	if err == nil || !strings.Contains(err.Error(), "token was refused") || strings.Contains(err.Error(), strings.Repeat("b", 35)) {
		t.Errorf("bad token: %v", err)
	}
	TelegramAPI = "http://127.0.0.1:1"
	if err := SendTelegram(context.Background(), Telegram{Token: tok, Chat: "1"}, "x"); err == nil || strings.Contains(err.Error(), tok) {
		t.Errorf("no server: %v", err)
	}
}

// An SMTP server without STARTTLS gets nothing: no login, no message.
func TestSendMailNeedsTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	heard := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		var all []string
		c.Write([]byte("220 test ESMTP\r\n"))
		for {
			l, err := rd.ReadString('\n')
			if err != nil {
				break
			}
			all = append(all, strings.TrimSpace(l))
			switch {
			case strings.HasPrefix(l, "EHLO"):
				c.Write([]byte("250-test\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(l, "QUIT"):
				c.Write([]byte("221 bye\r\n"))
			default:
				c.Write([]byte("250 ok\r\n"))
			}
		}
		heard <- strings.Join(all, "|")
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	m := Mail{Host: "127.0.0.1", User: "u", Pass: "secret-pass", From: "p@example.com", To: []string{"a@example.com"}}
	m.Port = atoi(port)
	err = SendMail(context.Background(), m, "s", "t")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("sent without TLS: %v", err)
	}
	ln.Close()
	if h := <-heard; strings.Contains(h, "AUTH") || strings.Contains(h, "MAIL") {
		t.Errorf("server heard %q", h)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestMimeWord(t *testing.T) {
	if mimeWord("Disk") != "Disk" || !strings.HasPrefix(mimeWord("Peringatan · disk"), "=?utf-8?b?") || strings.Contains(mimeWord("a\r\nBcc: x"), "\n") {
		t.Error("subject encoding")
	}
}
