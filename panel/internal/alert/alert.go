// Package alert sends the panel's alerts by Telegram and e-mail (OPS-01 wave 2). The bot token and the SMTP
// password are kept sealed with a key file of the panel (0600); they are never shown, logged or put in an error.
package alert

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Key seals and opens the stored secrets.
type Key struct{ k []byte }

// LoadKey reads the key file, or creates it (32 random bytes, 0600) when it does not exist.
func LoadKey(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
		return &Key{k: b}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("alert key file has the wrong size")
	}
	return &Key{k: b}, nil
}

// NewKey makes a key from 32 bytes (tests).
func NewKey(b []byte) *Key { return &Key{k: b} }

func (k *Key) gcm() (cipher.AEAD, error) {
	blk, err := aes.NewCipher(k.k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// Seal encrypts a secret for the settings table ("" stays "").
func (k *Key) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	g, err := k.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append(nonce, g.Seal(nil, nonce, []byte(plain), nil)...)), nil
}

// Open reverses Seal.
func (k *Key) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	g, err := k.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("short")
	}
	p, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	return string(p), err
}

// Telegram is a bot and the chat it writes to.
type Telegram struct {
	Token string
	Chat  string
}

// Mail is an SMTP account. Port 465 uses TLS from the start; any other port must offer STARTTLS.
type Mail struct {
	Host, User, Pass, From string
	Port                   int
	To                     []string
}

var (
	tokenRe = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{30,60}$`)
	chatRe  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)+[A-Za-z]{2,63}$`)
)

// CheckTelegram validates a bot token and chat.
func CheckTelegram(t Telegram) error {
	if !tokenRe.MatchString(t.Token) {
		return errors.New("the bot token looks like 123456789:AA… (from @BotFather)")
	}
	if !chatRe.MatchString(t.Chat) {
		return errors.New("the chat is a number (e.g. 123456789 or -100…) or a channel name like @mychannel")
	}
	return nil
}

// CheckMail validates an SMTP account.
func CheckMail(m Mail) error {
	if !hostRe.MatchString(m.Host) {
		return errors.New("the SMTP server is a host name like smtp.example.com")
	}
	if m.Port != 465 && m.Port != 587 && m.Port != 2525 {
		return errors.New("the port is 465 (TLS) or 587 / 2525 (STARTTLS)")
	}
	if len(m.User) > 200 || len(m.Pass) > 200 || strings.ContainsAny(m.User+m.Pass, "\r\n") {
		return errors.New("the login is too long or has a line break")
	}
	if a, err := mail.ParseAddress(m.From); err != nil || a.Name != "" {
		return errors.New("the sender is one e-mail address")
	}
	if len(m.To) == 0 || len(m.To) > 5 {
		return errors.New("one to five recipients")
	}
	for _, t := range m.To {
		if a, err := mail.ParseAddress(t); err != nil || a.Name != "" {
			return fmt.Errorf("%q is not an e-mail address", t)
		}
	}
	return nil
}

// TelegramAPI is the Bot API address (tests point it at a local server).
var TelegramAPI = "https://api.telegram.org"

// SendTelegram sends one message. Errors never carry the token (which is part of the address).
func SendTelegram(ctx context.Context, t Telegram, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"chat_id": {t.Chat}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, "POST", TelegramAPI+"/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("telegram: cannot reach api.telegram.org")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case 200:
		return nil
	case 401, 404:
		return errors.New("telegram: the bot token was refused")
	case 400, 403:
		return errors.New("telegram: the chat was refused (start the bot or add it to the chat first)")
	}
	return fmt.Errorf("telegram: answer %d", resp.StatusCode)
}

// SendMail sends one plain-text message over TLS (465) or STARTTLS; it never sends without encryption.
func SendMail(ctx context.Context, m Mail, subject, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	tcfg := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if m.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tcfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return errors.New("e-mail: cannot connect to " + addr)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return errors.New("e-mail: the server did not answer as SMTP")
	}
	defer c.Close()
	if m.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("e-mail: the server offers no STARTTLS; not sending without encryption")
		}
		if err := c.StartTLS(tcfg); err != nil {
			return errors.New("e-mail: STARTTLS failed (certificate?)")
		}
	}
	if m.User != "" {
		if err := c.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
			return errors.New("e-mail: the login was refused")
		}
	}
	if err := c.Mail(m.From); err != nil {
		return errors.New("e-mail: the sender was refused")
	}
	for _, t := range m.To {
		if err := c.Rcpt(t); err != nil {
			return errors.New("e-mail: a recipient was refused")
		}
	}
	w, err := c.Data()
	if err != nil {
		return errors.New("e-mail: the server refused the message")
	}
	msg := "From: " + m.From + "\r\nTo: " + strings.Join(m.To, ", ") + "\r\nSubject: " + mimeWord(subject) +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n") + "\r\n"
	if _, err := io.WriteString(w, msg); err != nil {
		return errors.New("e-mail: sending failed")
	}
	if err := w.Close(); err != nil {
		return errors.New("e-mail: the server refused the message")
	}
	_ = c.Quit()
	return nil
}

func mimeWord(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 126 {
			return "=?utf-8?b?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}
