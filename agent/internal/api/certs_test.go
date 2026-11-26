package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func TestCertRoutes(t *testing.T) {
	c, logPath := serve(t, map[int]bool{os.Getuid(): true})
	_, out := do(t, c, "POST", "/v1/sites", good)
	job := out["job"].(string)
	for i := 0; i < 300; i++ {
		if _, st := do(t, c, "GET", "/v1/jobs/"+job, ""); st["state"] != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, out := do(t, c, "GET", "/v1/sites/voltix-id/cert", ""); code != 200 || out["own"] != false {
		t.Fatalf("no cert %d %v", code, out)
	}
	crt, key := selfSigned(t, "voltix.id")
	body, _ := json.Marshal(map[string]string{"cert": crt, "key": key})
	code, out := do(t, c, "PUT", "/v1/sites/voltix-id/cert", string(body))
	if code != 200 || out["own"] != true || out["trusted"] != false {
		t.Fatalf("set %d %v", code, out)
	}
	_, text, _ := raw(t, c, "GET", "/v1/sites/voltix-id/cert", "", "")
	if strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, strings.Split(key, "\n")[1]) {
		t.Fatal("the key is in an answer")
	}
	other, okey := selfSigned(t, "example.org")
	body, _ = json.Marshal(map[string]string{"cert": other, "key": okey})
	if code, out := do(t, c, "PUT", "/v1/sites/voltix-id/cert", string(body)); code != 400 || !strings.Contains(out["error"].(string), "does not cover voltix.id") {
		t.Fatalf("other name %d %v", code, out)
	}
	if code, _ := do(t, c, "PUT", "/v1/sites/voltix-id/cert", `{"cert":"x","key":"y","extra":1}`); code != 400 {
		t.Fatalf("unknown field %d", code)
	}
	if code, _ := do(t, c, "PUT", "/v1/sites/nosuch/cert", `{"cert":"x","key":"y"}`); code != 404 {
		t.Fatalf("unknown site %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/cert/delete", ""); code != 200 {
		t.Fatalf("remove %d", code)
	}
	if code, _ := do(t, c, "POST", "/v1/sites/voltix-id/cert/delete", ""); code != 404 {
		t.Fatalf("second remove %d", code)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "cert-set") || !strings.Contains(string(b), "voltix.id until ") || strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("audit:\n%s", b)
	}
}
