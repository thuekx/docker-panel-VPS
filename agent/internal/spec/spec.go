// Package spec checks every input the agent accepts. Anything not matching is refused; nothing is cleaned up
// or guessed, so the panel always sees why a request failed.
package spec

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
)

// Limits on what one request may carry.
const (
	MaxAliases   = 10
	MaxTitle     = 80
	MaxDomainLen = 253
)

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	tldRe   = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	idRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	// Title: letters, digits, space and common punctuation; no quotes, backslashes, `$` or control characters.
	titleRe = regexp.MustCompile(`^[\p{L}\p{N} .,:;!?&()/+_@#%-]+$`)
)

// Domain checks a lowercase DNS host name with at least two labels. IP addresses, wildcards, trailing dots,
// upper case and IDN in unicode form are refused (IDN must be given as punycode, "xn--...").
func Domain(d string) error {
	if d == "" {
		return errors.New("domain is empty")
	}
	if len(d) > MaxDomainLen {
		return errors.New("domain is too long")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return errors.New("domain needs at least one dot")
	}
	for i, l := range labels {
		if !labelRe.MatchString(l) {
			return fmt.Errorf("domain label %q is not valid", l)
		}
		if len(l) >= 4 && l[2:4] == "--" && !strings.HasPrefix(l, "xn--") {
			return fmt.Errorf("domain label %q is not valid", l)
		}
		if i == len(labels)-1 && !tldRe.MatchString(l) && !strings.HasPrefix(l, "xn--") {
			return errors.New("domain must end in a name, not a number")
		}
	}
	return nil
}

// ID checks a site id: lowercase letters, digits and inner dashes, 1-40 characters.
func ID(id string) error {
	if !idRe.MatchString(id) || strings.Contains(id, "--") {
		return errors.New("site id is not valid")
	}
	return nil
}

// Email checks a plain address (no display name), at most 254 characters, with a valid domain part.
func Email(e string) error {
	if len(e) > 254 || strings.ContainsAny(e, " <>\"'\\") {
		return errors.New("e-mail is not valid")
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || a.Name != "" {
		return errors.New("e-mail is not valid")
	}
	at := strings.LastIndexByte(e, '@')
	if at < 1 {
		return errors.New("e-mail is not valid")
	}
	if Domain(strings.ToLower(e[at+1:])) != nil {
		return errors.New("e-mail domain is not valid")
	}
	return nil
}

// Title checks the WordPress site title.
func Title(t string) error {
	if t == "" || len([]rune(t)) > MaxTitle || !titleRe.MatchString(t) {
		return errors.New("title may hold letters, digits, spaces and simple punctuation, at most 80 characters")
	}
	return nil
}

// Apps a website can run (APP-01). WordPress is the default when none is given.
const (
	AppWordPress = "wordpress"
	AppPHP       = "php"     // plain PHP, optional MariaDB
	AppStatic    = "static"  // HTML / built React, Vue, Svelte, Astro, served by Nginx
	AppLaravel   = "laravel" // Laravel on Nginx + PHP-FPM with MariaDB, a queue worker and the scheduler
)

// Create is the body of a create-site request.
type Create struct {
	App        string   `json:"app,omitempty"` // wordpress (default) | php | laravel | static
	Domain     string   `json:"domain"`
	Aliases    []string `json:"aliases"`
	Primary    string   `json:"primary"` // "domain" (bare) or "www"
	Server     string   `json:"server"`  // apache | nginx (static: nginx)
	PHP        string   `json:"php"`     // from catalog (static: empty)
	AdminEmail string   `json:"admin_email"`
	Title      string   `json:"title"`
	Database   bool     `json:"database,omitempty"` // php: give the app its own MariaDB
}

// AppOf returns the app of a request, WordPress when none is given.
func (c Create) AppOf() string {
	if c.App == "" {
		return AppWordPress
	}
	return c.App
}

// Check validates a create request against the catalogue. preview is the preview zone (may be empty) and
// reserved holds host names already in use (panel domain, other sites).
func (c Create) Check(reserved map[string]bool) error {
	if err := Domain(c.Domain); err != nil {
		return err
	}
	if strings.HasPrefix(c.Domain, "www.") {
		return errors.New("give the domain without www.; choose www as primary instead")
	}
	if len(c.Aliases) > MaxAliases {
		return fmt.Errorf("at most %d aliases", MaxAliases)
	}
	seen := map[string]bool{c.Domain: true, "www." + c.Domain: true}
	for _, a := range c.Aliases {
		if err := Domain(a); err != nil {
			return fmt.Errorf("alias: %w", err)
		}
		if seen[a] {
			return fmt.Errorf("alias %s is listed twice", a)
		}
		seen[a] = true
	}
	for h := range seen {
		if reserved[h] {
			return fmt.Errorf("%s is already in use on this server", h)
		}
	}
	switch c.Primary {
	case "domain", "www":
	default:
		return errors.New("primary must be domain or www")
	}
	app := c.AppOf()
	switch app {
	case AppWordPress, AppPHP:
		switch c.Server {
		case "apache", "nginx":
		default:
			return errors.New("web server must be apache or nginx")
		}
		if _, ok := catalog.PHP(c.PHP); !ok {
			return fmt.Errorf("PHP %q is not offered", c.PHP)
		}
	case AppLaravel:
		if c.Server != "nginx" {
			return errors.New("a Laravel website runs on nginx")
		}
		if _, ok := catalog.PHP(c.PHP); !ok {
			return fmt.Errorf("PHP %q is not offered", c.PHP)
		}
	case AppStatic:
		if c.Server != "nginx" || c.PHP != "" {
			return errors.New("a static site runs on nginx without PHP")
		}
	default:
		return errors.New("app must be wordpress, php, laravel or static")
	}
	if c.Database && app != AppPHP {
		return errors.New("only a PHP app chooses its database (WordPress and Laravel always have one, a static site none)")
	}
	if app != AppWordPress {
		if c.AdminEmail != "" {
			return errors.New("only WordPress takes an admin e-mail")
		}
		if c.Title == "" {
			return nil
		}
		return Title(c.Title)
	}
	if err := Email(c.AdminEmail); err != nil {
		return err
	}
	return Title(c.Title)
}

// Hosts lists every host name a site answers on, primary first.
func (c Create) Hosts() []string {
	bare, www := c.Domain, "www."+c.Domain
	h := []string{bare, www}
	if c.Primary == "www" {
		h = []string{www, bare}
	}
	return append(h, c.Aliases...)
}
