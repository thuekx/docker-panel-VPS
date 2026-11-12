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
	AppNode      = "node"    // Node.js / Next.js app built from a Git repository (APP-02)
)

// Limits of the Node.js deploy settings.
const (
	MaxRepo    = 300
	MaxBranch  = 100
	MaxCommand = 300
)

var (
	// Only public https Git addresses: no user name or password in the address, no query, no fragment.
	repoRe   = regexp.MustCompile(`^https://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~-]+)+/?$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	dirRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`)
)

// Where a Node.js app's source comes from.
const (
	SourceGit   = "git"   // a public Git repository and branch
	SourceFiles = "files" // the source/ folder of the website, filled with the File Manager
)

// AppDir checks the folder of the app inside the repository or the source folder ("" = the top).
func AppDir(d string) error {
	if d == "" {
		return nil
	}
	if len(d) > 200 || !dirRe.MatchString(d) {
		return errors.New("app folder must be a relative path such as apps/web, without .. or a leading /")
	}
	for _, part := range strings.Split(d, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, "..") {
			return errors.New("app folder must be a relative path such as apps/web, without .. or a leading /")
		}
	}
	return nil
}

// Repo checks a public Git repository address (https only).
func Repo(r string) error {
	if r == "" || len(r) > MaxRepo || !repoRe.MatchString(r) || strings.Contains(r, "..") {
		return errors.New("repository must be a public https address such as https://github.com/owner/app.git, without a user name or password")
	}
	return nil
}

// Branch checks a Git branch name.
func Branch(b string) error {
	if b == "" || len(b) > MaxBranch || !branchRe.MatchString(b) || strings.Contains(b, "..") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "//") {
		return errors.New("branch name is not valid")
	}
	return nil
}

// Command checks a build or start command: one line of printable text. It runs inside the website's own
// container (the owner's own code runs there anyway), never on the server itself.
func Command(c string) error {
	if strings.TrimSpace(c) == "" || len(c) > MaxCommand {
		return fmt.Errorf("command must be 1-%d characters", MaxCommand)
	}
	for _, r := range c {
		if r < 0x20 || r == 0x7f {
			return errors.New("command must be one line without control characters")
		}
	}
	return nil
}

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
	Version    string   `json:"version,omitempty"`  // laravel: major version ("" = newest); node: Node.js version
	Source     string   `json:"source,omitempty"`   // node: git (default) | files
	Dir        string   `json:"dir,omitempty"`      // node: app folder inside the repository or source folder
	Repo       string   `json:"repo,omitempty"`     // node: public https Git repository
	Branch     string   `json:"branch,omitempty"`   // node: branch to deploy
	Build      string   `json:"build,omitempty"`    // node: build command ("" = default)
	Start      string   `json:"start,omitempty"`    // node: start command ("" = default)
}

// Default commands of a Node.js app. Install uses the lock file when there is one.
const (
	NodeInstall = "if [ -f package-lock.json ]; then npm ci; else npm install; fi"
	NodeBuild   = "npm run build --if-present"
	NodeStart   = "npm start"
)

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
		v := c.Version
		if v == "" {
			v = catalog.LaravelDefault
		}
		l, ok := catalog.Laravel(v)
		if !ok {
			return fmt.Errorf("Laravel %q is not offered", c.Version)
		}
		if phpLess(c.PHP, l.MinPHP) {
			return fmt.Errorf("Laravel %s needs PHP %s or newer", l.Version, l.MinPHP)
		}
	case AppStatic:
		if c.Server != "nginx" || c.PHP != "" {
			return errors.New("a static site runs on nginx without PHP")
		}
	case AppNode:
		if c.PHP != "" || c.Server != "" {
			return errors.New("a Node.js app runs without a web server choice or PHP")
		}
		if _, ok := catalog.Node(c.Version); !ok {
			return fmt.Errorf("Node.js %q is not offered", c.Version)
		}
		switch c.Source {
		case "", SourceGit:
			if err := Repo(c.Repo); err != nil {
				return err
			}
			if err := Branch(c.Branch); err != nil {
				return err
			}
		case SourceFiles:
			if c.Repo != "" || c.Branch != "" {
				return errors.New("a website built from its own files takes no repository or branch")
			}
		default:
			return errors.New("source must be git or files")
		}
		if err := AppDir(c.Dir); err != nil {
			return err
		}
		for _, cmd := range []string{c.Build, c.Start} {
			if cmd == "" {
				continue
			}
			if err := Command(cmd); err != nil {
				return err
			}
		}
	default:
		return errors.New("app must be wordpress, php, laravel, static or node")
	}
	if c.Version != "" && app != AppLaravel && app != AppNode {
		return errors.New("only Laravel and Node.js take a version")
	}
	if app != AppNode && (c.Repo != "" || c.Branch != "" || c.Build != "" || c.Start != "" || c.Source != "" || c.Dir != "") {
		return errors.New("only a Node.js app takes a repository and commands")
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

// phpLess compares two PHP versions "major.minor".
func phpLess(a, b string) bool {
	var am, an, bm, bn int
	fmt.Sscanf(a, "%d.%d", &am, &an)
	fmt.Sscanf(b, "%d.%d", &bm, &bn)
	return am < bm || (am == bm && an < bn)
}
