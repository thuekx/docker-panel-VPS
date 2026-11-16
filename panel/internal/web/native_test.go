package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// NATIVE-01: the engine choice shows only when the server has the native engine, and is sent with the website.
func TestNativeEngineChoice(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	_, body, _ := e.get(e.c, "/sites/new")
	if strings.Contains(body, `name="engine"`) {
		t.Error("engine choice shown without the native engine")
	}
	f.mu.Lock()
	f.native = true
	f.mu.Unlock()
	_, body, _ = e.get(e.c, "/sites/new")
	if !strings.Contains(body, `name="engine" value="native"`) || !strings.Contains(body, "8.3, 8.4") || !strings.Contains(body, "Docker (recommended)") {
		t.Fatal("engine choice missing")
	}
	form := url.Values{"step": {"plan"}, "domain": {"voltix.id"}, "primary": {"domain"}, "server": {"apache"}, "php": {"8.4"},
		"admin_email": {"admin@example.test"}, "title": {"Voltix"}, "engine": {"native"}}
	code, body, _ := e.postWith(e.c, "/sites/new", form, body)
	if code != 200 || !strings.Contains(body, "Native (host Nginx &#43; PHP-FPM &#43; MariaDB)") || !strings.Contains(body, `name="engine" value="native"`) {
		t.Fatalf("approve card %d %s", code, body[strings.Index(body, "<main"):])
	}
	form.Set("step", "create")
	if code, _, _ := e.postWith(e.c, "/sites/new", form, body); code != http.StatusSeeOther {
		t.Fatalf("create %d", code)
	}
	f.mu.Lock()
	c := f.created[len(f.created)-1]
	f.mu.Unlock()
	if c.Engine != "native" || c.Server != "nginx" {
		t.Errorf("sent %+v (native always runs on Nginx)", c)
	}
	// Laravel stays on Docker even if the field is sent
	r, _ := http.NewRequest("POST", "/", nil)
	r.PostForm = url.Values{"app": {"laravel"}, "engine": {"native"}, "domain": {"voltix.id"}}
	if c, _ := formCreate(r); c.Engine != "" {
		t.Errorf("laravel native %+v", c)
	}
}

func TestNativeOverview(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	f.mu.Lock()
	f.engine = "native"
	f.mu.Unlock()
	_, body, _ := e.get(e.c, "/sites/voltix-id")
	if !strings.Contains(body, "Native (host Nginx &#43; PHP-FPM &#43; MariaDB)") {
		t.Error("Overview does not show the engine")
	}
}
