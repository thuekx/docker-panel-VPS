package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func TestCheckMIME(t *testing.T) {
	if typ, exts, err := checkMIME(" Application/X-Tex ", "tex, .LTX tex"); err != nil || typ != "application/x-tex" || strings.Join(exts, " ") != "tex ltx" {
		t.Errorf("%q %v %v", typ, exts, err)
	}
	for _, c := range [][2]string{{"text", "x"}, {"text/plain;charset=utf-8", "x"}, {"text/x", ""}, {"text/x", "a.b"}, {"text/x", "x;"}, {"text/x y", "x"}, {"text/x", "{x}"}} {
		if _, _, err := checkMIME(c[0], c[1]); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q accepted", c)
		}
	}
}

func TestWebRulesNginx(t *testing.T) {
	m, _, s, v := appSite(t, spec.Create{App: "php", Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4"})
	if v.State != "done" {
		t.Fatal(v)
	}
	secDo(t, m, s.ID, SecurityChange{Op: "errpage-on", Code: 404})
	secDo(t, m, s.ID, SecurityChange{Op: "errpage-on", Code: 503})
	secDo(t, m, s.ID, SecurityChange{Op: "mime-add", Type: "text/x-tex", Exts: "tex ltx"})
	secDo(t, m, s.ID, SecurityChange{Op: "mime-add", Type: "application/x-latex", Exts: "ltx"}) // ltx moves over
	if _, err := m.ChangeSecurity(s.ID, SecurityChange{Op: "errpage-on", Code: 404}); !errors.Is(err, ErrNoChange) {
		t.Errorf("twice: %v", err)
	}
	if _, err := m.ChangeSecurity(s.ID, SecurityChange{Op: "errpage-on", Code: 418}); !errors.Is(err, ErrBadInput) {
		t.Errorf("418: %v", err)
	}
	got, _ := m.Get(s.ID)
	var mimes []string
	for _, mm := range got.MIME {
		mimes = append(mimes, mm.Type+"="+strings.Join(mm.Exts, ","))
	}
	if strings.Join(mimes, ";") != "application/x-latex=ltx;text/x-tex=tex" || len(got.ErrorPages) != 2 {
		t.Fatalf("site %+v %+v", got.ErrorPages, got.MIME)
	}
	page := mustRead(t, filepath.Join(m.wpDir(s.ID), errDir, "404.html"))
	if !strings.Contains(page, "<b>404</b>") || !strings.Contains(page, "Page not found") {
		t.Errorf("starter page %s", page)
	}
	// an edited page is never overwritten
	_ = os.WriteFile(filepath.Join(m.wpDir(s.ID), errDir, "404.html"), []byte("mine"), 0o644)
	secDo(t, m, s.ID, SecurityChange{Op: "errpage-off", Code: 404})
	secDo(t, m, s.ID, SecurityChange{Op: "errpage-on", Code: 404})
	if p := mustRead(t, filepath.Join(m.wpDir(s.ID), errDir, "404.html")); p != "mine" {
		t.Errorf("page overwritten: %q", p)
	}
	conf := mustRead(t, filepath.Join(m.dir(s.ID), "nginx.conf"))
	for _, want := range []string{
		"    error_page 404 /.nostek-errors/404.html;\n    error_page 503 /.nostek-errors/503.html;\n    location ^~ /.nostek-errors/ { access_log off; }\n",
		"    types {\n        text/html html htm shtml;\n",
		"        application/x-latex ltx;\n        text/x-tex tex;\n    }\n" + rulesEnd + "\n}\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nginx.conf lacks %q\n%s", want, conf)
		}
	}
	if strings.Count(conf, rulesBegin) != 1 || !strings.Contains(conf, "fastcgi_pass php:9000;") {
		t.Errorf("rules doubled or server lost:\n%s", conf)
	}
	for _, c := range []SecurityChange{{Op: "errpage-off", Code: 404}, {Op: "errpage-off", Code: 503}, {Op: "mime-remove", Type: "text/x-tex"}, {Op: "mime-remove", Type: "application/x-latex"}} {
		secDo(t, m, s.ID, c)
	}
	if conf := mustRead(t, filepath.Join(m.dir(s.ID), "nginx.conf")); conf != NginxPHPConf {
		t.Errorf("not back to the standard:\n%s", conf)
	}
	v2, _ := m.Security(s.ID)
	if !v2.WebServer || v2.ErrorDir != "/.nostek-errors" || len(v2.ErrorCodes) != 8 || v2.MIME == nil {
		t.Errorf("view %+v", v2)
	}
}

func TestWebRulesApache(t *testing.T) {
	m, _, id := readySite(t) // WordPress on Apache
	secDo(t, m, id, SecurityChange{Op: "errpage-on", Code: 403})
	secDo(t, m, id, SecurityChange{Op: "mime-add", Type: "application/wasm", Exts: "wasm"})
	cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml"))
	if !strings.Contains(cf, "      - ./apache.conf:/etc/apache2/conf-enabled/zz-nostek.conf:ro\n") {
		t.Errorf("no apache mount:\n%s", cf)
	}
	if a := mustRead(t, filepath.Join(m.dir(id), "apache.conf")); !strings.Contains(a, "ErrorDocument 403 /.nostek-errors/403.html\n") || !strings.Contains(a, "AddType application/wasm .wasm\n") {
		t.Errorf("apache.conf %s", a)
	}
	secDo(t, m, id, SecurityChange{Op: "errpage-off", Code: 403})
	secDo(t, m, id, SecurityChange{Op: "mime-remove", Type: "application/wasm"})
	if cf := mustRead(t, filepath.Join(m.dir(id), "compose.yml")); strings.Contains(cf, "apache.conf") {
		t.Error("mount left without rules")
	}
}

func TestWebRulesOnlyWithWebServer(t *testing.T) {
	m, _, s, _ := appSite(t, spec.Create{App: "node", Domain: "voltix.id", Primary: "domain", Version: "24", Source: "files", Start: "node server.js"})
	if _, err := m.ChangeSecurity(s.ID, SecurityChange{Op: "errpage-on", Code: 404}); !errors.Is(err, ErrBadInput) {
		t.Errorf("node app: %v", err)
	}
	if v, _ := m.Security(s.ID); v.WebServer {
		t.Error("node app has a web server")
	}
}

func TestWebRulesNative(t *testing.T) {
	m, _, h, p := nativeTest(t)
	job, s, err := m.Create(context.Background(), nativeReq(spec.AppStatic))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, job)
	secDo(t, m, s.ID, SecurityChange{Op: "errpage-on", Code: 404})
	n := mustRead(t, p.nginx(s.ID))
	if !strings.Contains(n, "    error_page 404 /.nostek-errors/404.html;\n") || !strings.HasSuffix(n, rulesEnd+"\n}\n") {
		t.Errorf("native nginx:\n%s", n)
	}
	if !strings.Contains(h.all(), "nginx -t") {
		t.Errorf("nginx not checked: %s", h.all())
	}
}
