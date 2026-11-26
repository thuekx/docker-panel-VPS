package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
	"github.com/thuekx/docker-panel-VPS/panel/internal/alert"
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

// dnsRoutes answers the fake agent's ClouDNS and wildcard routes.
func (f *fakeAgent) dnsRoutes(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	b, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == "GET":
		write(200, f.dns)
	case r.URL.Path == "/v1/dns":
		f.dnsCalls = append(f.dnsCalls, string(b))
		if !strings.Contains(string(b), `"password":"right-pass"`) {
			write(400, map[string]string{"error": "ClouDNS says: Invalid authentication, incorrect auth-id or auth-password."})
			return
		}
		f.dns = agent.DNSConfig{Provider: "cloudns", SubAuthID: "4242", Configured: true, Since: 1790000000}
		write(200, f.dns)
	case r.URL.Path == "/v1/dns/delete":
		f.dnsCalls = append(f.dnsCalls, "delete")
		if f.wcOn != "" {
			write(400, map[string]string{"error": "these websites use a wildcard certificate: voltix-id"})
			return
		}
		f.dns = agent.DNSConfig{}
		write(200, map[string]bool{"ok": true})
	default:
		f.dnsCalls = append(f.dnsCalls, "wildcard "+string(b))
		var a struct{ Zone string }
		_ = json.Unmarshal(b, &a)
		f.wcOn = a.Zone
		f.dns.Sites = nil
		if a.Zone != "" {
			f.dns.Sites = []string{"voltix-id"}
		}
		write(202, map[string]string{"job": "jddd0000000000001"})
	}
}

func TestWildcardAndClouDNS(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/settings/certs"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	pages := func(stage string) {
		for _, lang := range []string{"en", "id"} {
			e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
			for _, p := range []string{"/settings/certs", "/settings/certs?done=set", "/settings/certs?done=removed", "/domains/voltix-id", "/settings"} {
				code, body, _ := e.get(e.c, p)
				if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
					t.Errorf("%s %s (%s): %d or missing key %v", stage, p, lang, code, missingKey.FindString(body))
				}
			}
		}
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	}
	pages("preview only")
	if _, body, _ := e.get(e.c, "/domains/voltix-id"); !strings.Contains(body, "still uses its preview address") {
		t.Fatal("no preview note")
	}
	f.wcZones = []string{"voltix.id"}
	if _, body, _ := e.get(e.c, "/domains/voltix-id"); !strings.Contains(body, `href="/settings/certs"`) {
		t.Fatal("no link to the ClouDNS settings")
	}

	// the sub-user: wrong password refused with ClouDNS's words, right one accepted; neither reaches the audit log
	if code, _, h := e.post(e.c, "/settings/certs", url.Values{"op": {"set"}, "sub_auth_id": {"4242"}, "password": {"wrong-pass"}}, "/settings/certs"); code != 303 || !strings.Contains(h.Get("Location"), "Invalid+authentication") {
		t.Fatalf("wrong password: %d %s", code, h.Get("Location"))
	}
	if code, _, h := e.post(e.c, "/settings/certs", url.Values{"op": {"set"}, "sub_auth_id": {""}, "password": {"x"}}, "/settings/certs"); code != 303 || !strings.Contains(h.Get("Location"), "err=") || len(f.dnsCalls) != 1 {
		t.Fatalf("empty id: %d %s %v", code, h.Get("Location"), f.dnsCalls)
	}
	resp, _ := e.c.PostForm(e.ts.URL+"/settings/certs", url.Values{"op": {"set"}, "sub_auth_id": {"4242"}, "password": {"right-pass"}})
	resp.Body.Close()
	if resp.StatusCode != 403 || len(f.dnsCalls) != 1 {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	if code, _, h := e.post(e.c, "/settings/certs", url.Values{"op": {"set"}, "sub_auth_id": {" 4242 "}, "password": {"right-pass"}}, "/settings/certs"); code != 303 || h.Get("Location") != "/settings/certs?done=set" {
		t.Fatalf("set: %d %s", code, h.Get("Location"))
	}
	if !strings.Contains(f.dnsCalls[1], `"sub_auth_id":"4242"`) || !strings.Contains(f.dnsCalls[1], `"provider":"cloudns"`) {
		t.Fatalf("call %s", f.dnsCalls[1])
	}
	_, body, _ := e.get(e.c, "/settings/certs")
	if !strings.Contains(body, "Sub-user 4242 in use since") || strings.Contains(body, "right-pass") || !strings.Contains(body, "Remove sub-user") {
		t.Fatalf("settings page:\n%s", body)
	}
	pages("provider set")

	// a wildcard for the website
	_, body, _ = e.get(e.c, "/domains/voltix-id")
	if !strings.Contains(body, `<option value="voltix.id">*.voltix.id</option>`) {
		t.Fatal("no zone choice")
	}
	if code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"wildcard"}, "zone": {"Voltix.ID"}}, "/domains/voltix-id"); code != 303 || h.Get("Location") != "/domains/voltix-id?job=jddd0000000000001#job" {
		t.Fatalf("wildcard: %d %s", code, h.Get("Location"))
	}
	if last := f.dnsCalls[len(f.dnsCalls)-1]; last != `wildcard {"zone":"voltix.id"}` {
		t.Fatalf("call %q", last)
	}
	pages("wildcard on")
	if _, body, _ = e.get(e.c, "/domains/voltix-id"); !strings.Contains(body, "Turn the wildcard off") {
		t.Fatal("no way to turn it off")
	}
	if _, body, _ = e.get(e.c, "/settings/certs"); !strings.Contains(body, `href="/domains/voltix-id#cert"`) || strings.Contains(body, "Remove sub-user") {
		t.Fatal("the sub-user can be removed while in use")
	}
	if code, _, h := e.post(e.c, "/settings/certs", url.Values{"op": {"remove"}}, "/settings/certs"); code != 303 || !strings.Contains(h.Get("Location"), "wildcard+certificate") {
		t.Fatalf("remove in use: %d %s", code, h.Get("Location"))
	}
	e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"wildcard"}, "zone": {""}}, "/domains/voltix-id")
	if code, _, h := e.post(e.c, "/settings/certs", url.Values{"op": {"remove"}}, "/settings/certs"); code != 303 || h.Get("Location") != "/settings/certs?done=removed" {
		t.Fatalf("remove: %d %s", code, h.Get("Location"))
	}
	var audit string
	rows, _ := e.st.DB.Query(`SELECT action || ' ' || detail FROM audit`)
	for rows.Next() {
		var s string
		rows.Scan(&s)
		audit += s + "\n"
	}
	rows.Close()
	for _, want := range []string{"dns.set cloudns sub-user 4242\n", "cert.wildcard voltix-id zone voltix.id", "cert.wildcard voltix-id off", "dns.remove cloudns"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	if strings.Contains(audit, "right-pass") || strings.Contains(audit, "wrong-pass") {
		t.Fatalf("audit holds a password:\n%s", audit)
	}
}

func TestCertificateAlert(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	e.srv.SetAlertKey(alert.NewKey([]byte("0123456789abcdef0123456789abcdef")))
	e.srv.alertSync = true
	got := fakeTelegram(t)
	sealed, _ := e.srv.akey.Seal(testTok)
	e.st.SetSetting(kTgChat, "1")
	e.st.SetSetting(kTgToken, sealed)
	e.st.SetSetting(kKindsOff, "disk,memory,backup,watchdog")
	f.cert = agent.CertInfo{Own: true, Names: []string{"voltix.id"}, Issuer: "Origin CA", NotAfter: e.clock.Add(40 * 24 * time.Hour).Unix()}
	e.srv.AlertTick(t.Context())
	if len(*got) != 0 {
		t.Fatalf("40 days left is no news: %q", *got)
	}
	f.cert.NotAfter = e.clock.Add(5*24*time.Hour + time.Hour).Unix()
	e.srv.alerts.certAt = time.Time{}
	e.srv.AlertTick(t.Context())
	if len(*got) != 1 || !strings.Contains((*got)[0], "(5 days left)") {
		t.Fatalf("%q", *got)
	}
	e.srv.alerts.certAt = time.Time{}
	e.srv.AlertTick(t.Context())
	if len(*got) != 1 {
		t.Fatalf("sent twice: %q", *got)
	}
	f.cert.NotAfter = e.clock.Add(-time.Hour).Unix() // another certificate, already over
	e.srv.AlertTick(t.Context())
	if len(*got) != 1 {
		t.Fatal("looked again within the hour")
	}
	e.srv.alerts.certAt = time.Time{}
	e.srv.AlertTick(t.Context())
	if len(*got) != 2 || !strings.Contains((*got)[1], "ended") {
		t.Fatalf("%q", *got)
	}
}

func TestCertRequestAndSelfSigned(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, q := range []string{"?done=csr", "?done=csrdeleted", "?done=selfsigned"} {
			if code, body, _ := e.get(e.c, "/domains/voltix-id"+q); code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
				t.Errorf("%s (%s): %d %v", q, lang, code, missingKey.FindString(body))
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	_, body, _ := e.get(e.c, "/domains/voltix-id")
	for _, want := range []string{"Make a certificate request (CSR)", `name="names" value="voltix.id" checked>`, `name="names" value="shop.voltix.id">`, "Self-signed certificate"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `value="voltix-id.preview.nostek.dev"`) {
		t.Error("the preview address is offered")
	}
	// no names: nothing reaches the agent
	if _, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"csr"}}, "/domains/voltix-id"); !strings.Contains(h.Get("Location"), "err=") || len(f.certCalls) != 0 {
		t.Fatalf("no names: %s %v", h.Get("Location"), f.certCalls)
	}
	code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"csr"}, "names": {"voltix.id", "www.voltix.id"}, "key": {"rsa2048"}, "org": {" PT Voltix "}, "country": {"ID"}}, "/domains/voltix-id")
	if code != 303 || h.Get("Location") != "/domains/voltix-id?done=csr#csr" || f.certCalls[0] != `CSR {"names":["voltix.id","www.voltix.id"],"key":"rsa2048","org":"PT Voltix","unit":"","city":"","state":"","country":"ID"}` {
		t.Fatalf("csr: %d %s %v", code, h.Get("Location"), f.certCalls)
	}
	_, body, _ = e.get(e.c, "/domains/voltix-id")
	for _, want := range []string{"Certificate request waiting.", "BEGIN CERTIFICATE REQUEST", "leave the key empty to use the key made for it", "Delete request"} {
		if !strings.Contains(body, want) {
			t.Errorf("waiting request lacks %q", want)
		}
	}
	// the certificate comes back without a key
	cert := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	if code, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"set"}, "cert": {cert}}, "/domains/voltix-id"); code != 303 || h.Get("Location") != "/domains/voltix-id?done=cert#cert" || !strings.Contains(f.certCalls[1], `"key":""`) {
		t.Fatalf("paired: %d %s %v", code, h.Get("Location"), f.certCalls)
	}
	if _, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"csr-delete"}}, "/domains/voltix-id"); h.Get("Location") != "/domains/voltix-id?done=csrdeleted#cert" {
		t.Fatalf("delete: %s", h.Get("Location"))
	}
	// self-signed
	if _, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"self-signed"}, "names": {"voltix.id"}, "days": {"x"}}, "/domains/voltix-id"); !strings.Contains(h.Get("Location"), "err=") {
		t.Fatalf("bad days: %s", h.Get("Location"))
	}
	if _, _, h := e.post(e.c, "/domains/voltix-id/cert", url.Values{"op": {"self-signed"}, "names": {"voltix.id"}, "key": {"ec256"}, "days": {"90"}}, "/domains/voltix-id"); h.Get("Location") != "/domains/voltix-id?done=selfsigned#cert" {
		t.Fatalf("self-signed: %s", h.Get("Location"))
	}
	if last := f.certCalls[len(f.certCalls)-1]; last != `SELF {"days":90,"key":"ec256","names":["voltix.id"]}` {
		t.Errorf("sent %s", last)
	}
	_, body, _ = e.get(e.c, "/domains/voltix-id")
	if !strings.Contains(body, `<span class="pill s-warn">Self-signed</span>`) || !strings.Contains(body, "signed by itself") {
		t.Errorf("self-signed card:\n%s", body)
	}
	_, audit, _ := e.get(e.c, "/audit")
	for _, want := range []string{"voltix-id voltix.id ec256", "cert.csr-delete", "cert.self-signed"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit lacks %q", want)
		}
	}
}
