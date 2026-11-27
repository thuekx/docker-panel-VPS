package sites

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertRequest(t *testing.T) {
	m, _, id := readySite(t)
	m.cfg.PanelUID = os.Getuid()
	for name, in := range map[string]CSRInput{
		"no names":      {},
		"other name":    {Names: []string{"example.org"}},
		"bad key":       {Names: []string{"voltix.id"}, KeyType: "dsa"},
		"bad country":   {Names: []string{"voltix.id"}, Country: "Indonesia"},
		"bad subject":   {Names: []string{"voltix.id"}, Org: "a\nb"},
		"wildcard deep": {Names: []string{"*.www.voltix.id.example"}},
		"no main name":  {Names: []string{"*.voltix.id"}},
	} {
		if _, err := m.CreateCSR(id, in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r, err := m.CreateCSR(id, CSRInput{Names: []string{"Voltix.id", "*.voltix.id", "voltix.id"}, KeyType: "rsa2048", Org: "PT Voltix", Country: "id"})
	if err != nil || !strings.Contains(r.CSR, "BEGIN CERTIFICATE REQUEST") || strings.Join(r.Names, ",") != "voltix.id,*.voltix.id" || r.KeyType != "rsa2048" || r.Created == 0 {
		t.Fatalf("csr %+v %v", r, err)
	}
	if strings.Contains(r.CSR, "PRIVATE") {
		t.Fatal("the key left the server")
	}
	blk, _ := pem.Decode([]byte(r.CSR))
	req, err := x509.ParseCertificateRequest(blk.Bytes)
	if err != nil || req.CheckSignature() != nil || req.Subject.CommonName != "voltix.id" || req.Subject.Country[0] != "ID" || req.Subject.Organization[0] != "PT Voltix" {
		t.Fatalf("parsed %+v %v", req.Subject, err)
	}
	if fi, err := os.Stat(filepath.Join(m.requestDir(id), "key.pem")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("request key %v %v", fi, err)
	}
	// a CA signs the request; the certificate comes back without a key and is paired with the waiting one
	ca := newCA(t)
	now := time.Now()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: req.Subject, DNSNames: req.DNSNames, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, req.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	signed := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	other, _ := ca.leaf(t, []string{"voltix.id"}, now.Add(-time.Hour), now.Add(30*24*time.Hour))
	if _, err := m.SetCert(id, other, nil); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "waiting request") {
		t.Fatalf("a certificate of another key: %v", err)
	}
	ci, err := m.SetCert(id, append(signed, ca.pem...), nil)
	if err != nil || !ci.Own || ci.SelfSign || len(ci.Names) != 2 {
		t.Fatalf("paired %+v %v", ci, err)
	}
	if r, _ := m.CertRequest(id); r.CSR != "" {
		t.Fatal("the request was kept after its certificate came")
	}
	if _, err := os.Stat(m.requestDir(id)); err == nil {
		t.Fatal("request folder kept")
	}
	// a new request while a certificate is served: removing the certificate keeps the request
	if _, err := m.CreateCSR(id, CSRInput{Names: []string{"www.voltix.id", "voltix.id"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCert(id); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.CertRequest(id); r.KeyType != "ec256" || r.Names[0] != "www.voltix.id" {
		t.Fatalf("request after remove %+v", r)
	}
	if err := m.DeleteCSR(id); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteCSR(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete %v", err)
	}
}

func TestSelfSigned(t *testing.T) {
	m, _, id := readySite(t)
	m.cfg.PanelUID = os.Getuid()
	if _, err := m.SelfSigned(id, []string{"voltix.id"}, "", 0); !errors.Is(err, ErrBadInput) {
		t.Fatalf("0 days: %v", err)
	}
	if _, err := m.SelfSigned(id, []string{"voltix.id"}, "", SelfSignedDays+1); !errors.Is(err, ErrBadInput) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := m.SelfSigned(id, []string{"example.org"}, "", 30); !errors.Is(err, ErrBadInput) {
		t.Fatalf("other name: %v", err)
	}
	ci, err := m.SelfSigned(id, []string{"voltix.id", "www.voltix.id"}, "rsa2048", 365)
	if err != nil || !ci.Own || ci.Trusted || !ci.SelfSign || ci.Issuer != "voltix.id" || len(ci.Names) != 2 {
		t.Fatalf("self-signed %+v %v", ci, err)
	}
	if d := time.Unix(ci.NotAfter, 0).Sub(time.Now()); d < 364*24*time.Hour || d > 366*24*time.Hour {
		t.Fatalf("valid %v", d)
	}
	if _, err := os.Stat(m.edgeCertFile(id)); err != nil {
		t.Fatal("not served")
	}
}
