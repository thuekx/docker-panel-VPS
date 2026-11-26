package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestCertificateCard(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"", "?done=cert", "?done=certremoved"} {
			code, body, _ := e.get(e.c, "/domains/voltix-id"+q)
			if code != 200 || missingKey.MatchString(body) || !strings.Contains(body, `id="cert"`) {
				t.Errorf("%s (%s): %d or missing key %v", q, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	const key = "-----BEGIN PRIVATE KEY-----\nSECRETKEYMATERIAL\n-----END PRIVATE KEY-----"
	cert := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	if code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"set"}, "cert": {cert}}, "/domains/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "err=") || len(f.certCalls) != 0 {
		t.Fatalf("no key: %d %s %v", code, h.Get("Location"), f.certCalls)
	}
	if code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"set"}, "cert": {"BAD"}, "key": {key}}, "/domains/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), "does+not+belong") {
		t.Fatalf("refused: %d %s", code, h.Get("Location"))
	}
	code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"set"}, "cert": {cert}, "key": {key}}, "/domains/voltix-id")
	if code != 303 || h.Get("Location") != "/domains/voltix-id?done=cert#cert" || len(f.certCalls) != 1 || !strings.Contains(f.certCalls[0], "SECRETKEYMATERIAL") {
		t.Fatalf("set: %d %s %v", code, h.Get("Location"), f.certCalls)
	}
	_, body, _ := e.get(e.c, "/domains/voltix-id")
	for _, want := range []string{"Own certificate from Origin CA.", "Not publicly trusted", "<code>voltix.id</code>", "Remove own certificate"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "SECRETKEYMATERIAL") {
		t.Fatal("key on the page")
	}
	resp, _ := e.c.PostForm(e.ts.URL+"/domains/voltix-id/cert", url.Values{"op": {"remove"}})
	resp.Body.Close()
	if resp.StatusCode != 403 || len(f.certCalls) != 1 {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	if code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"remove"}}, "/domains/voltix-id"); code != 303 || h.Get("Location") != "/domains/voltix-id?done=certremoved#cert" {
		t.Fatalf("remove %d %s", code, h.Get("Location"))
	}
	var audit string
	rows, _ := e.st.DB.Query(`SELECT action || ' ' || detail FROM audit`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		audit += s + "\n"
	}
	rows.Close()
	if !strings.Contains(audit, "cert.set voltix-id voltix.id until 2030-03-17") || !strings.Contains(audit, "cert.remove voltix-id") || strings.Contains(audit, "SECRETKEY") {
		t.Fatalf("audit:\n%s", audit)
	}
}
