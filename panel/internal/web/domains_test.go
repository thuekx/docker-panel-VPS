package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// domains answers the domain routes of the fake agent.
func (f *fakeAgent) domains(w http.ResponseWriter, r *http.Request, write func(int, any)) {
	if strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
		if f.domJob == nil {
			write(404, map[string]string{"error": "job not found"})
			return
		}
		write(200, f.domJob)
		return
	}
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.domCalls = append(f.domCalls, strings.TrimSpace(string(b)))
		if strings.Contains(string(b), `"name":"bad"`) {
			write(400, map[string]string{"error": "bad input: domain needs at least one dot"})
			return
		}
		if f.domSame {
			write(200, map[string]string{"job": ""})
			return
		}
		write(202, map[string]string{"job": "jddd0000000000001"})
		return
	}
	write(200, agent.Domains{PublicIPv4: "203.0.113.10", PreviewZone: "preview.nostek.dev", Canonical: "voltix-id.preview.nostek.dev", MaxAliases: 10, Job: f.domJob,
		Hosts: []agent.DomainCheck{
			{Name: "voltix.id", Role: "primary", DNS: "here", IPv4: []string{"203.0.113.10"}, IPv6: []string{"2001:db8::7"}},
			{Name: "www.voltix.id", Role: "www", DNS: "elsewhere", IPv4: []string{"198.51.100.7"}},
			{Name: "shop.voltix.id", Role: "alias", DNS: "none"},
			{Name: "old.voltix.id", Role: "alias", Routed: true, DNS: "mixed", Cert: "error", CertError: "x509: certificate is valid for TRAEFIK DEFAULT CERT"},
			{Name: "voltix-id.preview.nostek.dev", Role: "preview", Routed: true, DNS: "here", Cert: "valid", CertIssuer: "R13", CertExpires: time.Now().Add(80 * 24 * time.Hour).Unix()},
		},
		Redirects: []agent.Redirect{{From: "/old-page", To: "/new-page", Code: 301}},
		Wildcard:  f.wcOn, WildcardZones: f.wcZones, DNSProvider: f.dns.Configured})
}

func TestDomainsPage(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	if code, _, h := e.get(e.c, "/domains/voltix-id"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("reachable without login: %d", code)
	}
	e.setupAdmin(e.c, "admin@example.test")
	jobs := []*agent.Job{nil,
		{ID: "jddd0000000000001", Kind: "domains", Site: "voltix-id", State: "running", Steps: []string{"routes", "addresses"}},
		{ID: "jddd0000000000001", Kind: "domains", Site: "voltix-id", State: "failed", Steps: []string{"routes", "addresses"}, Error: "rewriting the addresses failed; the website keeps its previous address"},
		{ID: "jddd0000000000001", Kind: "domains", Site: "voltix-id", State: "done", Steps: []string{"routes", "addresses", "check"}},
	}
	for _, lang := range []string{"en", "id"} {
		e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {lang}})
		for _, j := range jobs {
			f.domJob = j
			for _, q := range []string{"", "?job=jddd0000000000001", "?done=nochange"} {
				code, body, _ := e.get(e.c, "/domains/voltix-id"+q)
				if code != 200 || missingKey.MatchString(body) || strings.Contains(body, "%!") {
					t.Errorf("%s (%s, %v): %d or missing key %v", q, lang, j, code, missingKey.FindString(body))
				}
			}
		}
	}
	e.c.PostForm(e.ts.URL+"/lang", url.Values{"lang": {"en"}})
	f.domJob = nil
	_, body, _ := e.get(e.c, "/domains/voltix-id")
	for _, want := range []string{
		`href="/domains/voltix-id" aria-current="page"`,
		"DNS now points to this server (203.0.113.10). Press Check DNS again to serve it.",
		"Change its A record to 203.0.113.10",
		"Add an A record with 203.0.113.10",
		"Keep a single A record with 203.0.113.10",
		"An AAAA (IPv6) record points elsewhere",
		"AAAA 2001:db8::7",
		"1 name(s) point here and can be served now.",
		"2 of 10 extra names used.",
		"<code>/old-page</code> → <code>/new-page</code>",
		"valid until",
		`name="name" value="shop.voltix.id"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `name="name" value="voltix.id"`) || strings.Contains(body, `name="name" value="voltix-id.preview.nostek.dev"`) {
		t.Error("the main domain or preview can be removed")
	}
	f.domJob = jobs[3]
	if _, body, _ = e.get(e.c, "/domains/voltix-id?job=jddd0000000000001"); !strings.Contains(body, "Done. The change is live.") || !strings.Contains(body, "WordPress addresses moved") {

		t.Error("finished job not shown")
	}
	f.domJob = nil

	for _, c := range []struct {
		form url.Values
		loc  string
	}{
		{url.Values{"op": {"add"}, "name": {" Shop.Voltix.id "}}, "/domains/voltix-id?job=jddd0000000000001#job"},
		{url.Values{"op": {"add"}, "name": {""}}, "err="},
		{url.Values{"op": {"add"}, "name": {"bad"}}, "err=bad+input"},
		{url.Values{"op": {"remove"}, "name": {"shop.voltix.id"}}, "err="},
		{url.Values{"op": {"remove"}, "name": {"shop.voltix.id"}, "confirm": {"1"}}, "#job"},
		{url.Values{"op": {"refresh"}}, "#job"},
		{url.Values{"op": {"redirect-add"}, "from": {"/a"}, "to": {""}, "code": {"301"}}, "err="},
		{url.Values{"op": {"redirect-add"}, "from": {"/a"}, "to": {"https://example.com/"}, "code": {"302"}}, "#job"},
		{url.Values{"op": {"redirect-add"}, "from": {"/kb"}, "to": {"https://kb.example.com"}, "code": {"301"}, "prefix": {"1"}}, "#job"},
		{url.Values{"op": {"redirect-remove"}, "from": {"/old-page"}}, "#job"},
	} {
		if code, _, h := e.post(e.c, "/domains/voltix-id", c.form, "/domains/voltix-id"); code != 303 || !strings.Contains(h.Get("Location"), c.loc) {
			t.Errorf("%v: %d %s", c.form, code, h.Get("Location"))
		}
	}
	f.domSame = true
	if _, _, h := e.post(e.c, "/domains/voltix-id", url.Values{"op": {"refresh"}}, "/domains/voltix-id"); h.Get("Location") != "/domains/voltix-id?done=nochange" {
		t.Errorf("no change: %s", h.Get("Location"))
	}
	if code, _, _ := e.post(e.c, "/domains/voltix-id", url.Values{"op": {"nosuch"}}, "/domains/voltix-id"); code != 400 {
		t.Errorf("odd op: %d", code)
	}
	want := []string{`{"op":"add","name":"shop.voltix.id"}`, `{"op":"add","name":"bad"}`, `{"op":"remove","name":"shop.voltix.id"}`, `{"op":"refresh"}`,
		`{"op":"redirect-add","from":"/a","to":"https://example.com/","code":302}`,
		`{"op":"redirect-add","from":"/kb","to":"https://kb.example.com","code":301,"prefix":true}`, `{"op":"redirect-remove","from":"/old-page"}`, `{"op":"refresh"}`}
	if strings.Join(f.domCalls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(f.domCalls, "\n"))
	}
	_, audit, _ := e.get(e.c, "/audit")
	if !strings.Contains(audit, "voltix-id add shop.voltix.id") || !strings.Contains(audit, "redirect-add /a to https://example.com/ (302)") ||
		!strings.Contains(audit, "redirect-add /kb to https://kb.example.com (301) folder") {
		t.Error("audit lacks the domain changes")
	}
}
