package sites

// Wildcard certificates through the DNS challenge (CERT-01, CERT-1 = ClouDNS). The edge's resolver "dns" (lego,
// provider cloudns) reads an API sub-user's id and password from two files the agent writes, readable by the
// edge's user only; they are never in an answer, the compose file, an environment value or a log. A website then
// asks for a wildcard of one DNS zone (zone + *.zone), which covers its names in that zone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

const (
	edgeContainer = "nostek-core-edge-1"
	cloudnsAPI    = "https://api.cloudns.net"
)

var subAuthRe = regexp.MustCompile(`^[0-9]{1,12}$`)

// DNSConfig is the DNS provider the edge uses for wildcard certificates.
type DNSConfig struct {
	Provider   string   `json:"provider"` // cloudns, or "" when none is set
	SubAuthID  string   `json:"sub_auth_id,omitempty"`
	Configured bool     `json:"configured"`
	Since      int64    `json:"since,omitempty"`
	Sites      []string `json:"sites,omitempty"`      // websites with a wildcard
	EdgeError  string   `json:"edge_error,omitempty"` // the last edge restart failed with this
}

func (m *Manager) dnsDir() string      { return filepath.Join(m.cfg.Data, "edge", "dns") }
func (m *Manager) dnsIDFile() string   { return filepath.Join(m.dnsDir(), "cloudns.sub-auth-id") }
func (m *Manager) dnsPassFile() string { return filepath.Join(m.dnsDir(), "cloudns.password") }

func (m *Manager) cloudns() string {
	if m.cloudnsURL != "" {
		return m.cloudnsURL
	}
	return cloudnsAPI
}

// DNS returns the DNS provider settings (the password never).
func (m *Manager) DNS() (DNSConfig, error) {
	c := DNSConfig{}
	id, err1 := os.ReadFile(m.dnsIDFile())
	fi, err2 := os.Stat(m.dnsPassFile())
	if err1 == nil && err2 == nil {
		c.Provider, c.SubAuthID, c.Configured, c.Since = "cloudns", strings.TrimSpace(string(id)), true, fi.ModTime().Unix()
	}
	list, err := m.List()
	if err != nil {
		return c, err
	}
	for _, s := range list {
		if s.Wildcard != "" {
			c.Sites = append(c.Sites, s.ID)
		}
	}
	return c, nil
}

// cloudnsCall posts one ClouDNS API call (the password goes in the body, never in an address) and returns an
// error with ClouDNS's own text when it says Failed.
func (m *Manager) cloudnsCall(ctx context.Context, path, id, pass string, extra url.Values) error {
	v := url.Values{"sub-auth-id": {id}, "auth-password": {pass}}
	for k, x := range extra {
		v[k] = x
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", m.cloudns()+path, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: ClouDNS cannot be reached", ErrBadInput)
	}
	defer resp.Body.Close()
	var a struct {
		Status      string `json:"status"`
		Description string `json:"statusDescription"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &a) == nil && strings.EqualFold(a.Status, "Failed") {
		return fmt.Errorf("%w: ClouDNS says: %s", ErrBadInput, a.Description)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: ClouDNS answered %d", ErrBadInput, resp.StatusCode)
	}
	return nil
}

// SetDNS checks a ClouDNS API sub-user and gives it to the edge; the edge is restarted (a few seconds) so it reads
// the new files. The restart comes a moment after the answer: the panel's own page goes through the edge.
func (m *Manager) SetDNS(ctx context.Context, id, pass string) error {
	id = strings.TrimSpace(id)
	if !subAuthRe.MatchString(id) {
		return fmt.Errorf("%w: the sub-auth-id is a number (ClouDNS → API & Resellers → API sub-users)", ErrBadInput)
	}
	if pass == "" || len(pass) > 256 || strings.ContainsAny(pass, "\r\n") {
		return fmt.Errorf("%w: the password is empty, too long or has a line break", ErrBadInput)
	}
	if err := m.cloudnsCall(ctx, "/dns/login.json", id, pass, nil); err != nil {
		return err
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.dnsDir(), 0o755); err != nil {
		return err
	}
	for f, v := range map[string]string{m.dnsIDFile(): id, m.dnsPassFile(): pass} {
		if err := writeFile(f, []byte(v), 0o600); err != nil {
			return err
		}
		if err := os.Chown(f, m.panelUID(), m.panelUID()); err != nil {
			return err
		}
	}
	m.restartEdgeSoon()
	return nil
}

// edgeDelay is how long the edge restart waits for the answer to reach the browser.
var edgeDelay = 2 * time.Second

func (m *Manager) restartEdgeSoon() {
	go func() {
		time.Sleep(edgeDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		msg := ""
		if out, err := m.run.Docker(ctx, m.cfg.Data, nil, "restart", edgeContainer); err != nil {
			msg = "the edge did not restart: " + tail(out)
		}
		m.edgeMu.Lock()
		m.edgeErr = msg
		m.edgeMu.Unlock()
	}()
}

// RemoveDNS takes the sub-user away again; refused while a website uses a wildcard.
func (m *Manager) RemoveDNS(ctx context.Context) error {
	c, err := m.DNS()
	if err != nil {
		return err
	}
	if len(c.Sites) > 0 {
		return fmt.Errorf("%w: these websites use a wildcard certificate: %s", ErrBadInput, strings.Join(c.Sites, ", "))
	}
	if !c.Configured {
		return ErrNotFound
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	for _, f := range []string{m.dnsIDFile(), m.dnsPassFile()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	m.restartEdgeSoon()
	return nil
}

// WildcardZones are the zones a website may take a wildcard of: its main name and the names above it (at least
// two labels), when its main name is its own domain.
func (s Site) WildcardZones() []string {
	c := s.Canonical()
	for _, h := range s.Hosts {
		if h.Name == c && h.Role == "preview" {
			return nil
		}
	}
	var out []string
	labels := strings.Split(c, ".")
	for i := 0; i+2 <= len(labels); i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// StartWildcard turns a website's wildcard certificate on (zone) or off (""): a job "domains" with one step.
func (m *Manager) StartWildcard(ctx context.Context, id, zone string) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	zone = strings.ToLower(strings.TrimSpace(zone))
	if zone == s.Wildcard {
		return "", fmt.Errorf("%w: nothing to change", ErrBadInput)
	}
	if zone != "" {
		ok := false
		for _, z := range s.WildcardZones() {
			ok = ok || z == zone
		}
		if !ok || spec.Domain(zone) != nil {
			return "", fmt.Errorf("%w: %s is not a zone above the website's main name", ErrBadInput, zone)
		}
		id1, err1 := os.ReadFile(m.dnsIDFile())
		pw, err2 := os.ReadFile(m.dnsPassFile())
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("%w: set the ClouDNS API sub-user first (Settings → Certificates)", ErrBadInput)
		}
		if err := m.cloudnsCall(ctx, "/dns/get-zone-info.json", strings.TrimSpace(string(id1)), string(pw), url.Values{"domain-name": {zone}}); err != nil {
			return "", fmt.Errorf("%w (zone %s)", err, zone)
		}
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("domains", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		next := s
		next.Wildcard = zone
		j.Step("routes")
		if err := m.writeRoutes(ctx, next); err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

// UseClouDNS points ClouDNS calls at another address (the API package's tests; a real agent never calls it).
func (m *Manager) UseClouDNS(u string) { m.cloudnsURL = u }
