// Package ext holds the extension points of the panel (EXT-01): an add-on built on the panel, as a Go program that
// imports github.com/thuekx/docker-panel-VPS/panel/app, can let people other than the server's admin sign in and use
// only the websites it allows them, and can add its own pages. The panel works fully without any extension: every
// field is optional and a user that is not an admin can do nothing unless an extension allows it.
//
// What the panel always enforces, whatever an extension says:
//   - users whose role is not "admin" get 404 for every server-wide page (settings, server disk, audit log, trash,
//     updates, API keys …) and for any website page Access does not allow: deny by default, so a page added to the
//     panel later stays admin-only until it is listed for other users;
//   - some website actions stay admin-only because they reach outside the website (see AdminOnly);
//   - sign-in, two-factor login, CSRF checks and the audit log are the panel's own and cannot be skipped.
package ext

import (
	"context"
	"html/template"
	"net/http"
)

// RoleAdmin is the role of the server's admins; every other role is limited by Access.
const RoleAdmin = "admin"

// User is a signed-in panel user.
type User struct {
	ID    int64
	OrgID int64
	Email string
	Role  string
}

// Admin reports whether u is one of the server's admins.
func (u User) Admin() bool { return u.Role == RoleAdmin }

// Request is one use of a website by a user that is not an admin.
type Request struct {
	Site   string // the website's id
	Page   string // the route, e.g. "GET /files/{id}" or "POST /sites/{id}"
	Action string // for a form post, the form's "op" (or "action") value; "" otherwise
}

// Create is a website a user that is not an admin wants to make.
type Create struct {
	Domain string
	App    string // "" for WordPress, else the app kind (php, static, laravel, node …)
	Engine string // "docker" or "native"
}

// Event is something that happened to a website.
type Event string

const (
	SiteCreated Event = "created" // a create job started; the website's id is known
	SiteDeleted Event = "deleted" // the website went to the trash
)

// MenuItem is an extra entry of the panel's menu.
type MenuItem struct {
	Href  string // a path of the extension, "/x/<name>/…"
	Label string
}

// Site is a website as an extension sees it.
type Site struct {
	ID, Domain, App, Status string
}

// GroupLimits are the limits a website group shares: CPU (100 = one core), memory in MB and processes.
type GroupLimits struct {
	CPU, Memory, Tasks int
}

// Group is a website group: its limits, its websites and the memory it uses now in MB (-1 = unknown).
type Group struct {
	Name string
	GroupLimits
	Sites   []string
	MemUsed int
}

// Host is what an extension may ask of the panel.
type Host interface {
	// User is the signed-in user of r (only inside the extension's own pages).
	User(r *http.Request) (User, bool)
	// CSRF is the token every form of the extension must send as the field "csrf".
	CSRF(r *http.Request) string
	// Lang is the user's language ("id" or "en").
	Lang(r *http.Request) string
	// Page draws body inside the panel's layout with the given title. body must be made with html/template.
	Page(w http.ResponseWriter, r *http.Request, title string, body template.HTML)
	// Audit writes the audit log as the signed-in user of r.
	Audit(r *http.Request, action, detail string)
	// NewUser makes a user of the admin's organisation with a random password and returns its id and a one-time
	// link path ("/reveal/…", 15 minutes) that shows the password once. The user sets up two-factor login at the first sign-in.
	NewUser(r *http.Request, email, role string) (int64, string, error)
	// SetLocked locks or unlocks a user that is not an admin. Locking signs the user out everywhere.
	SetLocked(r *http.Request, id int64, locked bool) error
	// DeleteUser removes a user that is not an admin and signs it out.
	DeleteUser(r *http.Request, id int64) error
	// Sites lists every website of the server.
	Sites(ctx context.Context) ([]Site, error)
	// SiteAction starts or stops a website ("start", "stop").
	SiteAction(ctx context.Context, id, action string) error
	// DeleteSite moves a website to the trash; it returns the job id.
	DeleteSite(ctx context.Context, id string) (string, error)
	// Groups lists the website groups. A group's websites share its CPU, memory and process limits (each
	// website's own limits still apply inside them).
	Groups(ctx context.Context) ([]Group, error)
	// SetGroup makes a group or changes its limits; they apply at once. A name is 1-24 of a-z0-9.
	SetGroup(ctx context.Context, name string, l GroupLimits) error
	// DeleteGroup removes a group that has no website.
	DeleteGroup(ctx context.Context, name string) error
	// SetSiteGroup moves a website into a group ("" = none); the website restarts in it.
	SetSiteGroup(ctx context.Context, site, group string) error
}

// Route is an extension page, served at "/x/<name><Path>" for the signed-in users the handler itself accepts.
// Pattern is a method and a path, e.g. "GET /accounts" or "POST /accounts/{id}". The panel checks sign-in,
// two-factor login and, for POST, the CSRF field before the handler runs.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
}

// Extensions are the extension points. The zero value is the plain panel.
type Extensions struct {
	// Name is the extension's short name ([a-z0-9]{2,16}); its pages are under /x/<name>/.
	Name string
	// Access decides whether u, who is not an admin, may make the request. nil allows nothing.
	Access func(u User, req Request) bool
	// CheckCreate decides whether u, who is not an admin, may make the website; the error text is shown on the
	// form. nil means only admins make websites.
	CheckCreate func(u User, c Create) error
	// OnSite is told about websites made or deleted by any user.
	OnSite func(ev Event, u User, site string)
	// Start runs once when the panel starts, with the panel's Host; Routes and Menu are read after it.
	Start func(h Host) error
	// Routes are the extension's pages.
	Routes []Route
	// Menu gives the extra menu entries for u.
	Menu func(u User) []MenuItem
}

// AdminOnly are website actions only admins may use, whatever Access allows: they reach outside the website
// (resource limits, the server firewall, the server's DNS account, a second website).
var AdminOnly = map[string][]string{
	"POST /sites/{id}":          {"limits"},
	"POST /db/{id}":             {"remote"},
	"POST /domains/{id}/cert":   {"wildcard"},
	"POST /wp/{id}":             {"staging", "push"},
	"GET /terminal/{id}":        nil, // nil: the whole page
	"POST /terminal/{id}":       nil,
	"GET /terminal/{id}/socket": nil,
	"GET /stack/{id}":           nil,
}
