package sites

// Certificate requests and self-signed certificates (CERT-02).
//
// A CSR is made on the server: the private key is generated here, kept in the website folder (tls/request/,
// 0600) and never answered. Only the CSR goes to the certificate authority; the certificate it sends back is
// uploaded without a key, and the agent pairs it with the waiting key. A self-signed certificate is made the same
// way and served at once, for test names and private networks (browsers warn about it).

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// CSRInput is what a certificate request asks for.
type CSRInput struct {
	Names   []string `json:"names"`   // the first is the common name
	KeyType string   `json:"key"`     // ec256 (default) | rsa2048 | rsa3072
	Org     string   `json:"org"`     // optional subject fields
	Unit    string   `json:"unit"`    //
	City    string   `json:"city"`    //
	State   string   `json:"state"`   //
	Country string   `json:"country"` // two letters
}

// CertRequest is a waiting request: its CSR (public) and what it asks for, never the key.
type CertRequest struct {
	CSR     string   `json:"csr"`
	Names   []string `json:"names"`
	KeyType string   `json:"key"`
	Created int64    `json:"created"`
}

// KeyTypes are the key choices of a request or a self-signed certificate.
var KeyTypes = []string{"ec256", "rsa2048", "rsa3072"}

// SelfSignedDays bounds a self-signed certificate (browsers refuse leaf certificates longer than 825 days).
const SelfSignedDays = 825

var subjectRe = regexp.MustCompile(`^[\p{L}\p{N} .,&'()/_-]{0,64}$`)

func (m *Manager) requestDir(id string) string { return filepath.Join(m.certDir(id), "request") }

// certNamesFor checks the names of a request against the website: each is one of its own names (not the preview
// address) or a wildcard of one.
func certNamesFor(s Site, names []string) ([]string, error) {
	own := map[string]bool{}
	for _, h := range s.Hosts {
		if h.Role != "preview" {
			own[h.Name] = true
		}
	}
	if len(own) == 0 {
		return nil, fmt.Errorf("%w: the website has only its preview address; add its own domain first", ErrBadInput)
	}
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || slices.Contains(out, n) {
			continue
		}
		if !own[n] && !own[strings.TrimPrefix(n, "*.")] {
			return nil, fmt.Errorf("%w: %s is not a name of this website", ErrBadInput, n)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: choose at least one name", ErrBadInput)
	}
	if len(out) > 20 {
		return nil, fmt.Errorf("%w: at most 20 names", ErrBadInput)
	}
	// like coversWebsite: the main name, or one own name while the main name is the preview address
	want := []string{s.Canonical()}
	if !own[s.Canonical()] {
		want = slices.Collect(maps.Keys(own))
	}
	for _, w := range want {
		for _, n := range out {
			if n == w || (strings.HasPrefix(n, "*.") && strings.Count(w, ".") > 1 && w[strings.Index(w, ".")+1:] == n[2:]) {
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: the names must include the main name %s", ErrBadInput, s.Canonical())
}

func newKey(kind string) (crypto.Signer, []byte, error) {
	var k crypto.Signer
	var err error
	switch kind {
	case "", "ec256":
		k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "rsa2048":
		k, err = rsa.GenerateKey(rand.Reader, 2048)
	case "rsa3072":
		k, err = rsa.GenerateKey(rand.Reader, 3072)
	default:
		return nil, nil, fmt.Errorf("%w: key must be one of %v", ErrBadInput, KeyTypes)
	}
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func (in CSRInput) subject(cn string) (pkix.Name, error) {
	for _, v := range []string{in.Org, in.Unit, in.City, in.State} {
		if !subjectRe.MatchString(v) {
			return pkix.Name{}, fmt.Errorf("%w: subject fields are at most 64 letters, digits, spaces or .,&'()/_-", ErrBadInput)
		}
	}
	c := strings.ToUpper(strings.TrimSpace(in.Country))
	if c != "" && !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(c) {
		return pkix.Name{}, fmt.Errorf("%w: the country is two letters (ID, SG, US)", ErrBadInput)
	}
	n := pkix.Name{CommonName: cn}
	add := func(dst *[]string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = []string{v}
		}
	}
	add(&n.Organization, in.Org)
	add(&n.OrganizationalUnit, in.Unit)
	add(&n.Locality, in.City)
	add(&n.Province, in.State)
	add(&n.Country, c)
	return n, nil
}

// CreateCSR makes a key and a request for a website and keeps the key until the certificate comes back. A new
// request replaces a waiting one.
func (m *Manager) CreateCSR(id string, in CSRInput) (CertRequest, error) {
	s, err := m.Get(id)
	if err != nil {
		return CertRequest{}, err
	}
	names, err := certNamesFor(s, in.Names)
	if err != nil {
		return CertRequest{}, err
	}
	subj, err := in.subject(names[0])
	if err != nil {
		return CertRequest{}, err
	}
	k, keyPEM, err := newKey(in.KeyType)
	if err != nil {
		return CertRequest{}, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: subj, DNSNames: names}, k)
	if err != nil {
		return CertRequest{}, err
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if !m.mu.TryLock() {
		return CertRequest{}, ErrBusy
	}
	defer m.mu.Unlock()
	d := m.requestDir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return CertRequest{}, err
	}
	_ = os.Chmod(m.certDir(id), 0o700)
	if err := writeFile(filepath.Join(d, "key.pem"), keyPEM, 0o600); err != nil {
		return CertRequest{}, err
	}
	if err := writeFile(filepath.Join(d, "csr.pem"), csr, 0o644); err != nil {
		return CertRequest{}, err
	}
	return m.CertRequest(id)
}

// CertRequest returns the waiting request of a website (CSR empty when there is none).
func (m *Manager) CertRequest(id string) (CertRequest, error) {
	if _, err := m.Get(id); err != nil {
		return CertRequest{}, err
	}
	p := filepath.Join(m.requestDir(id), "csr.pem")
	b, err := os.ReadFile(p)
	if err != nil {
		return CertRequest{}, nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return CertRequest{}, nil
	}
	r, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil {
		return CertRequest{}, nil
	}
	v := CertRequest{CSR: string(b), Names: r.DNSNames, KeyType: "ec256"}
	if k, ok := r.PublicKey.(*rsa.PublicKey); ok {
		v.KeyType = fmt.Sprintf("rsa%d", k.N.BitLen())
	}
	if fi, err := os.Stat(p); err == nil {
		v.Created = fi.ModTime().Unix()
	}
	return v, nil
}

// DeleteCSR throws a waiting request and its key away.
func (m *Manager) DeleteCSR(id string) error {
	if _, err := m.Get(id); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.requestDir(id), "csr.pem")); err != nil {
		return fmt.Errorf("%w: the website has no waiting request", ErrNotFound)
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	return os.RemoveAll(m.requestDir(id))
}

// requestKey is the key of the waiting request, when it belongs to certPEM's first certificate.
func (m *Manager) requestKey(id string, certPEM []byte) []byte {
	k, err := os.ReadFile(filepath.Join(m.requestDir(id), "key.pem"))
	if err != nil {
		return nil
	}
	if _, _, err := parseCertLoose(certPEM, k); err != nil {
		return nil
	}
	return k
}

// SelfSigned makes a key and a self-signed certificate for names of the website and serves it at once.
func (m *Manager) SelfSigned(id string, names []string, keyType string, days int) (CertInfo, error) {
	s, err := m.Get(id)
	if err != nil {
		return CertInfo{}, err
	}
	names, err = certNamesFor(s, names)
	if err != nil {
		return CertInfo{}, err
	}
	if days < 1 || days > SelfSignedDays {
		return CertInfo{}, fmt.Errorf("%w: days must be 1 to %d", ErrBadInput, SelfSignedDays)
	}
	k, keyPEM, err := newKey(keyType)
	if err != nil {
		return CertInfo{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return CertInfo{}, err
	}
	now := m.now()
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0], Organization: []string{"Nostek Panel self-signed"}},
		DNSNames: names, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if _, ok := k.(*rsa.PrivateKey); ok {
		tpl.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	if err != nil {
		return CertInfo{}, err
	}
	return m.SetCert(id, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM)
}
