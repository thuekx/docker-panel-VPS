package sites

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T) testCA {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{c, k, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf makes a certificate for names, valid from nb to na, and its key, both PEM.
func (ca testCA) leaf(t *testing.T, names []string, nb, na time.Time) ([]byte, []byte) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: nb, NotAfter: na, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &k.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func TestOwnCertificate(t *testing.T) {
	m, _, id := readySite(t)
	m.cfg.PanelUID = os.Getuid()
	ca := newCA(t)
	now := time.Now()
	good, key := ca.leaf(t, []string{"voltix.id", "www.voltix.id"}, now.Add(-time.Hour), now.Add(40*24*time.Hour))
	other, otherKey := ca.leaf(t, []string{"example.org"}, now.Add(-time.Hour), now.Add(40*24*time.Hour))
	old, oldKey := ca.leaf(t, []string{"voltix.id"}, now.Add(-48*time.Hour), now.Add(time.Hour))
	early, earlyKey := ca.leaf(t, []string{"voltix.id"}, now.Add(time.Hour), now.Add(40*24*time.Hour))
	_, wrongKey := ca.leaf(t, []string{"voltix.id"}, now.Add(-time.Hour), now.Add(40*24*time.Hour))
	caKeyDER, _ := x509.MarshalECPrivateKey(ca.key)
	caKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})
	for name, c := range map[string]struct{ cert, key []byte }{
		"empty":            {nil, key},
		"not pem":          {[]byte("hello"), key},
		"key mismatch":     {good, wrongKey},
		"expired soon":     {old, oldKey},
		"not yet valid":    {early, earlyKey},
		"other name":       {other, otherKey},
		"a CA as the leaf": {ca.pem, caKey},
		"key in cert":      {append(append([]byte{}, good...), key...), key},
		"too large":        {append(good, make([]byte, MaxCertPEM)...), key},
	} {
		if _, err := m.SetCert(id, c.cert, c.key); !errors.Is(err, ErrBadInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ci, _ := m.Cert(id); ci.Own {
		t.Fatal("a refused certificate was kept")
	}

	// a private CA: accepted, not trusted
	ci, err := m.SetCert(id, append(append([]byte{}, good...), ca.pem...), key)
	if err != nil || !ci.Own || ci.Trusted || ci.Issuer != "Test Root" || len(ci.Names) != 2 {
		t.Fatalf("set %+v %v", ci, err)
	}
	m.certRoots = x509.NewCertPool()
	m.certRoots.AddCert(ca.cert)
	if ci, _ = m.Cert(id); !ci.Trusted || ci.NotAfter == 0 {
		t.Fatalf("trusted with its root: %+v", ci)
	}
	kp := filepath.Join(m.edgeCertDir(), "site-"+id+".key")
	if fi, err := os.Stat(kp); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("edge key %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(m.certDir(id), "key.pem")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("stored key mode %v", fi.Mode())
	}
	y, _ := os.ReadFile(m.edgeCertFile(id))
	if !strings.Contains(string(y), "certFile: /dynamic/certs/site-"+id+".crt") || !strings.Contains(string(y), "keyFile: /dynamic/certs/site-"+id+".key") {
		t.Fatalf("edge file:\n%s", y)
	}

	// trash and back
	if _, err := m.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.edgeCertFile(id)); err == nil {
		t.Fatal("the edge still serves a deleted website's certificate")
	}
	tr, _ := m.Trash()
	if _, err := m.Restore(context.Background(), tr[0].Entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kp); err != nil {
		t.Fatal("certificate not back after a trash restore")
	}

	// a stray file is removed by the sync, a live one rewritten
	_ = os.WriteFile(filepath.Join(m.edgeCertDir(), "site-gone.key"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(m.cfg.Data, "edge", "dynamic", "cert-gone.yml"), []byte("x"), 0o644)
	_ = os.Remove(kp)
	if err := m.SyncCerts(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.edgeCertDir(), "site-gone.key")); err == nil {
		t.Fatal("stray key kept")
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Data, "edge", "dynamic", "cert-gone.yml")); err == nil {
		t.Fatal("stray file kept")
	}
	if _, err := os.Stat(kp); err != nil {
		t.Fatal("live key not rewritten")
	}

	if err := m.RemoveCert(id); err != nil {
		t.Fatal(err)
	}
	if ci, _ := m.Cert(id); ci.Own {
		t.Fatal("still own after remove")
	}
	if _, err := os.Stat(kp); err == nil {
		t.Fatal("edge key kept after remove")
	}
	if err := m.RemoveCert(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove %v", err)
	}
}
