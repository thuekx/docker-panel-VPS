package spec

import (
	"strings"
	"testing"
)

func TestDomain(t *testing.T) {
	good := []string{"voltix.id", "nostek.dev", "a.b.c.example.com", "xn--bcher-kva.example", "my-site.co.id", "x1.preview.nostek.dev"}
	bad := []string{"", "localhost", "Voltix.id", "voltix.id.", ".voltix.id", "vol tix.id", "-a.id", "a-.id", "a..id",
		"1.2.3.4", "*.voltix.id", "voltix.123", "ab--cd.id", "voltix.id/x", "voltix.id:80", "v`x.id", "v$x.id",
		strings.Repeat("a", 64) + ".id", strings.Repeat("abcdefghi.", 26) + "id"}
	for _, d := range good {
		if err := Domain(d); err != nil {
			t.Errorf("Domain(%q) = %v, want ok", d, err)
		}
	}
	for _, d := range bad {
		if Domain(d) == nil {
			t.Errorf("Domain(%q) ok, want refused", d)
		}
	}
}

func TestEmailTitleID(t *testing.T) {
	for _, e := range []string{"a@b.id", "first.last+wp@nostek.dev"} {
		if Email(e) != nil {
			t.Errorf("Email(%q) refused", e)
		}
	}
	for _, e := range []string{"", "a", "a@b", "A <a@b.id>", "a@b.id ", "a'@b.id", "a@-b.id", strings.Repeat("a", 250) + "@b.id"} {
		if Email(e) == nil {
			t.Errorf("Email(%q) ok, want refused", e)
		}
	}
	if Title("Nostek Blog (2026)!") != nil || Title("Toko Ünik") != nil {
		t.Error("plain title refused")
	}
	for _, s := range []string{"", `a"b`, "a'b", "a$b", "a`b", "a\\b", "a\nb", strings.Repeat("x", 81)} {
		if Title(s) == nil {
			t.Errorf("Title(%q) ok, want refused", s)
		}
	}
	for _, s := range []string{"voltix-id", "a", "nostek-dev-2"} {
		if ID(s) != nil {
			t.Errorf("ID(%q) refused", s)
		}
	}
	for _, s := range []string{"", "-a", "a-", "a--b", "A", "a.b", "../x", strings.Repeat("a", 41)} {
		if ID(s) == nil {
			t.Errorf("ID(%q) ok, want refused", s)
		}
	}
}

func okCreate() Create {
	return Create{Domain: "voltix.id", Primary: "domain", Server: "apache", PHP: "8.4", AdminEmail: "owner@nostek.dev", Title: "Voltix"}
}

func TestCreateCheck(t *testing.T) {
	if err := okCreate().Check(nil); err != nil {
		t.Fatalf("good request refused: %v", err)
	}
	cases := map[string]func(*Create){
		"www domain":   func(c *Create) { c.Domain = "www.voltix.id" },
		"bad server":   func(c *Create) { c.Server = "caddy" },
		"bad php":      func(c *Create) { c.PHP = "7.4" },
		"bad primary":  func(c *Create) { c.Primary = "both" },
		"bad email":    func(c *Create) { c.AdminEmail = "x" },
		"bad title":    func(c *Create) { c.Title = "$(id)" },
		"dup alias":    func(c *Create) { c.Aliases = []string{"voltix.co.id", "voltix.co.id"} },
		"alias is www": func(c *Create) { c.Aliases = []string{"www.voltix.id"} },
		"bad alias":    func(c *Create) { c.Aliases = []string{"bad_alias.id"} },
		"too many alias": func(c *Create) {
			c.Aliases = strings.Split("a.id b.id c.id d.id e.id f.id g.id h.id i.id j.id k.id", " ")
		},
	}
	for name, mod := range cases {
		c := okCreate()
		mod(&c)
		if c.Check(nil) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := okCreate()
	if c.Check(map[string]bool{"www.voltix.id": true}) == nil {
		t.Error("reserved host accepted")
	}
	c.Primary = "www"
	c.Aliases = []string{"voltix.co.id"}
	if h := c.Hosts(); strings.Join(h, ",") != "www.voltix.id,voltix.id,voltix.co.id" {
		t.Errorf("hosts = %v", h)
	}
}

func TestCreateApps(t *testing.T) {
	base := Create{Domain: "voltix.id", Primary: "domain", Server: "nginx", PHP: "8.4"}
	ok := []Create{
		func() Create { c := base; c.App = "static"; c.PHP = ""; return c }(),
		func() Create { c := base; c.App = "static"; c.PHP = ""; c.Title = "Voltix"; return c }(),
		func() Create { c := base; c.App = "php"; return c }(),
		func() Create { c := base; c.App = "php"; c.Server = "apache"; c.Database = true; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Title = "Shop"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Version = "12"; c.PHP = "8.2"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Version = "13"; c.PHP = "8.3"; return c }(),
	}
	for _, c := range ok {
		if err := c.Check(nil); err != nil {
			t.Errorf("%+v refused: %v", c, err)
		}
	}
	bad := []Create{
		func() Create { c := base; c.App = "static"; return c }(),                                  // PHP on a static site
		func() Create { c := base; c.App = "static"; c.PHP = ""; c.Server = "apache"; return c }(), // apache
		func() Create { c := base; c.App = "static"; c.PHP = ""; c.Database = true; return c }(),   // database
		func() Create { c := base; c.App = "php"; c.PHP = "7.4"; return c }(),
		func() Create { c := base; c.App = "php"; c.AdminEmail = "o@nostek.dev"; return c }(),
		func() Create { c := base; c.App = "php"; c.Title = "x$(id)"; return c }(),
		func() Create { c := base; c.App = "rails"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Version = "13"; c.PHP = "8.2"; return c }(), // too old PHP
		func() Create { c := base; c.App = "laravel"; c.PHP = "8.1"; return c }(),                   // newest needs 8.3
		func() Create { c := base; c.App = "laravel"; c.Version = "12"; c.PHP = "8.1"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Version = "10"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Version = "^13 || dev-master"; return c }(),
		func() Create { c := base; c.App = "php"; c.Version = "13"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Server = "apache"; return c }(),
		func() Create { c := base; c.App = "laravel"; c.PHP = ""; return c }(),
		func() Create { c := base; c.App = "laravel"; c.Database = true; return c }(),
		func() Create { c := base; c.App = "laravel"; c.AdminEmail = "o@nostek.dev"; return c }(),
		func() Create {
			c := base
			c.App = "wordpress"
			c.Database = true
			c.AdminEmail = "o@nostek.dev"
			c.Title = "V"
			return c
		}(),
		func() Create { c := base; return c }(), // WordPress needs e-mail and title
	}
	for _, c := range bad {
		if err := c.Check(nil); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if (Create{}).AppOf() != AppWordPress {
		t.Error("default app is not WordPress")
	}
}
