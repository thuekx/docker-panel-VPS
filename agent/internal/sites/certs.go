package sites

// Own certificates (CERT-01). A website may use a certificate of its own (bought elsewhere, a Cloudflare origin
// certificate, a company CA) in place of Let's Encrypt. It is kept in the website folder (tls/), so it moves with
// backups, the trash and a server backup, and copied to the edge's file provider, which serves it for the names it
// covers; the edge then asks Let's Encrypt for no name it covers. The private key is never answered or logged.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxCertPEM bounds an uploaded certificate chain and an uploaded key.
const MaxCertPEM = 64 << 10

// CertInfo describes a website's own certificate (no key material).
type CertInfo struct {
	Own       bool     `json:"own"`
	Names     []string `json:"names,omitempty"`
	Issuer    string   `json:"issuer,omitempty"`
	NotBefore int64    `json:"not_before,omitempty"`
	NotAfter  int64    `json:"not_after,omitempty"`
	Trusted   bool     `json:"trusted"`             // a public CA (the system roots) signs the chain
	Uncovered []string `json:"uncovered,omitempty"` // names of the website it does not cover (they keep Let's Encrypt)
	Uploaded  int64    `json:"uploaded,omitempty"`
}

func (m *Manager) certDir(id string) string { return filepath.Join(m.dir(id), "tls") }
func (m *Manager) edgeCertDir() string      { return filepath.Join(m.cfg.Data, "edge", "dynamic", "certs") }
func (m *Manager) edgeCertFile(id string) string {
	return filepath.Join(m.cfg.Data, "edge", "dynamic", "cert-"+id+".yml")
}

// parseCert checks a chain and key and returns the leaf, the intermediates and the pair.
func parseCert(certPEM, keyPEM []byte, now time.Time) (*x509.Certificate, *x509.CertPool, error) {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, nil, fmt.Errorf("%w: the certificate and its private key are both needed", ErrBadInput)
	}
	if len(certPEM) > MaxCertPEM || len(keyPEM) > MaxCertPEM {
		return nil, nil, fmt.Errorf("%w: the certificate or the key is too large", ErrBadInput)
	}
	var certs []*x509.Certificate
	rest := certPEM
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			return nil, nil, fmt.Errorf("%w: the certificate file holds a %s block; put the key in its own field", ErrBadInput, b.Type)
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: a certificate in the chain cannot be read", ErrBadInput)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, nil, fmt.Errorf("%w: the certificate is not PEM (-----BEGIN CERTIFICATE-----)", ErrBadInput)
	}
	if len(certs) > 8 {
		return nil, nil, fmt.Errorf("%w: the chain has too many certificates", ErrBadInput)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, fmt.Errorf("%w: the private key does not belong to the certificate, or it is not a PEM key", ErrBadInput)
	}
	leaf := certs[0]
	if leaf.IsCA {
		return nil, nil, fmt.Errorf("%w: the first certificate must be the website's, not a CA's", ErrBadInput)
	}
	if now.Before(leaf.NotBefore) {
		return nil, nil, fmt.Errorf("%w: the certificate is not valid yet (from %s)", ErrBadInput, leaf.NotBefore.UTC().Format("2006-01-02"))
	}
	if !now.Add(24 * time.Hour).Before(leaf.NotAfter) {
		return nil, nil, fmt.Errorf("%w: the certificate has expired or expires within a day (%s)", ErrBadInput, leaf.NotAfter.UTC().Format("2006-01-02"))
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	return leaf, inter, nil
}

func certNames(c *x509.Certificate) []string {
	if len(c.DNSNames) > 0 {
		return c.DNSNames
	}
	if c.Subject.CommonName != "" {
		return []string{c.Subject.CommonName}
	}
	return nil
}

// certInfo describes a certificate for a website.
func certInfo(s Site, leaf *x509.Certificate, inter *x509.CertPool, now time.Time, roots *x509.CertPool) CertInfo {
	ci := CertInfo{Own: true, Names: certNames(leaf), Issuer: leaf.Issuer.CommonName, NotBefore: leaf.NotBefore.Unix(), NotAfter: leaf.NotAfter.Unix()}
	if ci.Issuer == "" && len(leaf.Issuer.Organization) > 0 {
		ci.Issuer = leaf.Issuer.Organization[0]
	}
	_, err := leaf.Verify(x509.VerifyOptions{Intermediates: inter, Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	ci.Trusted = err == nil
	for _, h := range s.Hosts {
		if h.Role == "preview" {
			continue // the preview name lives under the panel's own zone
		}
		if leaf.VerifyHostname(h.Name) != nil {
			ci.Uncovered = append(ci.Uncovered, h.Name)
		}
	}
	return ci
}

// coversWebsite wants the main name covered, or, while the main name is the preview address, one of the website's
// own names; the preview address is under the panel's zone and keeps Let's Encrypt.
func coversWebsite(s Site, leaf *x509.Certificate) error {
	var own []string
	for _, h := range s.Hosts {
		if h.Role != "preview" {
			own = append(own, h.Name)
		}
	}
	if len(own) == 0 {
		return fmt.Errorf("%w: the website has only its preview address; add its own domain first", ErrBadInput)
	}
	want := own
	for _, n := range own {
		if n == s.Canonical() {
			want = []string{n}
		}
	}
	for _, n := range want {
		if leaf.VerifyHostname(n) == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: the certificate does not cover %s (it is for %s)", ErrBadInput, strings.Join(want, ", "), strings.Join(certNames(leaf), ", "))
}

// Cert returns a website's own certificate, or Own = false.
func (m *Manager) Cert(id string) (CertInfo, error) {
	s, err := m.Get(id)
	if err != nil {
		return CertInfo{}, err
	}
	cp, kp := filepath.Join(m.certDir(id), "cert.pem"), filepath.Join(m.certDir(id), "key.pem")
	c, err1 := os.ReadFile(cp)
	k, err2 := os.ReadFile(kp)
	if err1 != nil || err2 != nil {
		return CertInfo{}, nil
	}
	now := m.now()
	leaf, inter, err := parseCertLoose(c, k)
	if err != nil {
		return CertInfo{Own: true}, nil
	}
	ci := certInfo(s, leaf, inter, now, m.certRoots)
	if fi, err := os.Stat(cp); err == nil {
		ci.Uploaded = fi.ModTime().Unix()
	}
	return ci, nil
}

// parseCertLoose reads a stored pair without the time checks (a stored certificate may have expired since).
func parseCertLoose(certPEM, keyPEM []byte) (*x509.Certificate, *x509.CertPool, error) {
	b, rest := pem.Decode(certPEM)
	if b == nil {
		return nil, nil, errors.New("no certificate")
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, nil, err
	}
	inter := x509.NewCertPool()
	for {
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			inter.AddCert(c)
		}
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, nil, err
	}
	return leaf, inter, nil
}

// SetCert checks and stores a website's own certificate and serves it at once.
func (m *Manager) SetCert(id string, certPEM, keyPEM []byte) (CertInfo, error) {
	s, err := m.Get(id)
	if err != nil {
		return CertInfo{}, err
	}
	now := m.now()
	leaf, inter, err := parseCert(certPEM, keyPEM, now)
	if err != nil {
		return CertInfo{}, err
	}
	if err := coversWebsite(s, leaf); err != nil {
		return CertInfo{}, err
	}
	if !m.mu.TryLock() {
		return CertInfo{}, ErrBusy
	}
	defer m.mu.Unlock()
	d := m.certDir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return CertInfo{}, err
	}
	if err := writeFile(filepath.Join(d, "key.pem"), keyPEM, 0o600); err != nil {
		return CertInfo{}, err
	}
	if err := writeFile(filepath.Join(d, "cert.pem"), certPEM, 0o644); err != nil {
		return CertInfo{}, err
	}
	if err := m.writeEdgeCert(id); err != nil {
		return CertInfo{}, err
	}
	ci := certInfo(s, leaf, inter, now, m.certRoots)
	ci.Uploaded = now.Unix()
	return ci, nil
}

// RemoveCert takes a website back to Let's Encrypt.
func (m *Manager) RemoveCert(id string) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	if _, err := os.Stat(filepath.Join(m.certDir(id), "cert.pem")); err != nil {
		return fmt.Errorf("%w: the website has no own certificate", ErrNotFound)
	}
	m.dropEdgeCert(id)
	return os.RemoveAll(m.certDir(id))
}

// writeEdgeCert copies a website's pair to the edge (key readable by the edge's user only) and writes its file.
func (m *Manager) writeEdgeCert(id string) error {
	c, err := os.ReadFile(filepath.Join(m.certDir(id), "cert.pem"))
	if err != nil {
		return err
	}
	k, err := os.ReadFile(filepath.Join(m.certDir(id), "key.pem"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.edgeCertDir(), 0o755); err != nil {
		return err
	}
	kp := filepath.Join(m.edgeCertDir(), "site-"+id+".key")
	if err := writeFile(kp, k, 0o600); err != nil {
		return err
	}
	if err := os.Chown(kp, m.panelUID(), m.panelUID()); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(m.edgeCertDir(), "site-"+id+".crt"), c, 0o644); err != nil {
		return err
	}
	y := fmt.Sprintf("# Generated by nostek-agent for site %s (own certificate). Do not edit: the agent rewrites it.\n"+
		"tls:\n  certificates:\n    - certFile: /dynamic/certs/site-%s.crt\n      keyFile: /dynamic/certs/site-%s.key\n", id, id, id)
	return writeFile(m.edgeCertFile(id), []byte(y), 0o644)
}

func (m *Manager) dropEdgeCert(id string) {
	for _, f := range []string{m.edgeCertFile(id), filepath.Join(m.edgeCertDir(), "site-"+id+".crt"), filepath.Join(m.edgeCertDir(), "site-"+id+".key")} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = err
		}
	}
}

// SyncCerts makes the edge's certificate files follow the website folders: every live website with tls/ is
// served, every other file goes (agent start, after a delete, a trash restore and a server restore).
func (m *Manager) SyncCerts() error {
	list, err := m.List()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	var errs []string
	for _, s := range list {
		if _, err := os.Stat(filepath.Join(m.certDir(s.ID), "cert.pem")); err != nil {
			continue
		}
		want[s.ID] = true
		if err := m.writeEdgeCert(s.ID); err != nil {
			errs = append(errs, s.ID+": "+err.Error())
		}
	}
	ents, _ := os.ReadDir(filepath.Join(m.cfg.Data, "edge", "dynamic"))
	for _, e := range ents {
		if id, ok := strings.CutSuffix(strings.TrimPrefix(e.Name(), "cert-"), ".yml"); ok && strings.HasPrefix(e.Name(), "cert-") && !want[id] {
			m.dropEdgeCert(id)
		}
	}
	ents, _ = os.ReadDir(m.edgeCertDir())
	for _, e := range ents {
		n := strings.TrimPrefix(e.Name(), "site-")
		id := strings.TrimSuffix(strings.TrimSuffix(n, ".crt"), ".key")
		if !want[id] {
			_ = os.Remove(filepath.Join(m.edgeCertDir(), e.Name()))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
