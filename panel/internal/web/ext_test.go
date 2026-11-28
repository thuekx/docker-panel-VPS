package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/thuekx/docker-panel-VPS/panel/ext"
	"github.com/thuekx/docker-panel-VPS/panel/internal/auth"
)

// testExt is a small extension: role "customer" may use voltix-id only, makes websites unless the domain is
// full.test, and has three pages (hello, make a user, lock a user).
type testExt struct {
	mu     sync.Mutex
	host   ext.Host
	events []string
	asked  []ext.Request
}

func (x *testExt) extensions() ext.Extensions {
	return ext.Extensions{
		Name: "hm",
		Start: func(h ext.Host) error {
			x.host = h
			return nil
		},
		Access: func(u ext.User, req ext.Request) bool {
			x.mu.Lock()
			defer x.mu.Unlock()
			x.asked = append(x.asked, req)
			return u.Role == "customer" && req.Site == "voltix-id"
		},
		CheckCreate: func(u ext.User, c ext.Create) error {
			if c.Domain == "full.test" {
				return errors.New("package full")
			}
			return nil
		},
		OnSite: func(ev ext.Event, u ext.User, site string) {
			x.mu.Lock()
			defer x.mu.Unlock()
			x.events = append(x.events, string(ev)+" "+u.Email+" "+site)
		},
		Menu: func(u ext.User) []ext.MenuItem {
			if u.Admin() {
				return []ext.MenuItem{{Href: "/x/hm/hello", Label: "Accounts"}, {Href: "/elsewhere", Label: "Bad"}}
			}
			return nil
		},
		Routes: []ext.Route{
			{Pattern: "GET /hello", Handler: func(w http.ResponseWriter, r *http.Request) {
				u, _ := x.host.User(r)
				body := template.HTML(template.HTMLEscapeString("hello "+u.Email) + `<form method="post" action="/x/hm/make"><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(x.host.CSRF(r)) + `"></form>`)
				x.host.Page(w, r, "Accounts", body)
			}},
			{Pattern: "POST /make", Handler: func(w http.ResponseWriter, r *http.Request) {
				id, link, err := x.host.NewUser(r, r.PostForm.Get("email"), r.PostForm.Get("role"))
				if err != nil {
					http.Error(w, err.Error(), 422)
					return
				}
				fmt.Fprintf(w, "id=%d link=%s", id, link)
			}},
			{Pattern: "POST /lock", Handler: func(w http.ResponseWriter, r *http.Request) {
				id, _ := strconv.ParseInt(r.PostForm.Get("id"), 10, 64)
				if err := x.host.SetLocked(r, id, r.PostForm.Get("on") == "1"); err != nil {
					http.Error(w, err.Error(), 422)
					return
				}
				w.Write([]byte("ok"))
			}},
		},
	}
}

// signInNew signs in a user made from a reveal link and enrols two-factor login.
func (e *env) signInNew(c *http.Client, email, link string) {
	e.t.Helper()
	code, body, _ := e.post(c, link, url.Values{}, link)
	m := regexp.MustCompile(`<code id="pw">([^<]+)</code>`).FindStringSubmatch(body)
	if code != 200 || m == nil {
		e.t.Fatalf("reveal: %d", code)
	}
	e.ipLimitReset()
	if code, _, h := e.post(c, "/login", url.Values{"email": {email}, "password": {m[1]}}, "/login"); code != 303 || h.Get("Location") != "/2fa/enrol" {
		e.t.Fatalf("login: %d %s", code, h.Get("Location"))
	}
	_, body, _ = e.get(c, "/2fa/enrol")
	sec := regexp.MustCompile(`class="secret">([A-Z2-7]+)<`).FindStringSubmatch(body)
	if sec == nil {
		e.t.Fatal("no enrol secret")
	}
	totp, _ := auth.TOTPCode(sec[1], e.clock)
	if code, _, _ := e.postWith(c, "/2fa/enrol", url.Values{"code": {totp}}, body); code != 200 {
		e.t.Fatalf("enrol: %d", code)
	}
}

func TestExtensionAccounts(t *testing.T) {
	e := newEnv(t)
	f := e.withAgent()
	f.stgCopy = true // a second website the customer may not see
	x := &testExt{}
	if err := e.srv.SetExtensions(x.extensions()); err != nil {
		t.Fatal(err)
	}
	e.setupAdmin(e.c, "admin@example.test")

	// the admin sees the extension's menu (only its own paths) and page
	code, body, _ := e.get(e.c, "/x/hm/hello")
	if code != 200 || !strings.Contains(body, "hello admin@example.test") || !strings.Contains(body, `href="/x/hm/hello"`) || strings.Contains(body, "/elsewhere") {
		t.Fatalf("extension page: %d", code)
	}
	if code, _, _ := e.get(e.c, "/x/hm/nothing"); code != 404 {
		t.Fatalf("unknown extension page: %d", code)
	}
	// POST needs the CSRF field
	if resp, _ := e.c.PostForm(e.ts.URL+"/x/hm/make", url.Values{"email": {"c@example.test"}, "role": {"customer"}}); resp.StatusCode != 403 {
		t.Fatalf("extension POST without CSRF: %d", resp.StatusCode)
	}
	// no admin through an extension
	if code, _, _ := e.post(e.c, "/x/hm/make", url.Values{"email": {"boss@example.test"}, "role": {"admin"}}, "/x/hm/hello"); code != 422 {
		t.Fatalf("admin made by extension: %d", code)
	}
	code, out, _ := e.post(e.c, "/x/hm/make", url.Values{"email": {"Cust@Example.test"}, "role": {"customer"}}, "/x/hm/hello")
	m := regexp.MustCompile(`id=(\d+) link=(/reveal/\S+)`).FindStringSubmatch(out)
	if code != 200 || m == nil {
		t.Fatalf("make user: %d %s", code, out)
	}
	if code, _, _ := e.post(e.c, "/x/hm/make", url.Values{"email": {"cust@example.test"}, "role": {"customer"}}, "/x/hm/hello"); code != 422 {
		t.Fatal("same e-mail twice")
	}

	cust := e.client()
	e.signInNew(cust, "cust@example.test", m[2])

	// the server view is for admins
	if code, _, h := e.get(cust, "/"); code != 303 || h.Get("Location") != "/sites" {
		t.Fatalf("dashboard for a customer: %d %s", code, h.Get("Location"))
	}
	code, body, _ = e.get(cust, "/sites")
	if code != 200 || !strings.Contains(body, "voltix.id") || strings.Contains(body, "staging.voltix.id") {
		t.Fatalf("customer list: %d", code)
	}
	if strings.Contains(body, `href="/settings"`) || strings.Contains(body, `href="/audit"`) || strings.Contains(body, `href="/disk"`) || !strings.Contains(body, `href="/profile"`) {
		t.Fatal("customer menu shows server pages")
	}
	if !strings.Contains(body, `href="/sites/new"`) {
		t.Fatal("CheckCreate set: the customer may make websites")
	}
	// deny by default: every server-wide page, other websites, admin-only website pages
	for _, p := range []string{"/settings", "/audit", "/disk", "/settings/api", "/settings/update", "/settings/backup",
		"/settings/server", "/settings/system", "/settings/certs", "/settings/quarantine", "/sites/staging-voltix-id",
		"/files/staging-voltix-id", "/terminal/voltix-id", "/stack/voltix-id", "/migrate-plugin/nostek-migrate.zip"} {
		if code, _, _ := e.get(cust, p); code != 404 {
			t.Errorf("%s for a customer: %d", p, code)
		}
	}
	for _, p := range []string{"/sites/voltix-id", "/files/voltix-id", "/domains/voltix-id", "/backup/voltix-id", "/profile", "/help"} {
		if code, _, _ := e.get(cust, p); code != 200 {
			t.Errorf("%s for its customer: %d", p, code)
		}
	}
	// website actions: allowed ones work, admin-only ones answer 404
	if code, _, _ := e.post(cust, "/sites/voltix-id", url.Values{"action": {"limits"}, "web": {"512"}}, "/sites/voltix-id"); code != 404 {
		t.Fatalf("limits by a customer: %d", code)
	}
	if code, _, _ := e.post(cust, "/wp/voltix-id", url.Values{"op": {"staging"}}, "/sites/voltix-id"); code != 404 {
		t.Fatalf("staging by a customer: %d", code)
	}
	if code, _, _ := e.post(cust, "/sites/staging-voltix-id", url.Values{"action": {"restart"}}, "/sites/voltix-id"); code != 404 {
		t.Fatalf("restart of another website: %d", code)
	}
	if code, _, _ := e.post(cust, "/sites/voltix-id", url.Values{"action": {"restart"}}, "/sites/voltix-id"); code != 303 || f.actions[len(f.actions)-1] != "restart" {
		t.Fatalf("restart by its customer: %d %v", code, f.actions)
	}
	x.mu.Lock()
	sawAction := false
	for _, a := range x.asked {
		sawAction = sawAction || (a.Page == "POST /sites/{id}" && a.Action == "restart" && a.Site == "voltix-id")
	}
	x.mu.Unlock()
	if !sawAction {
		t.Fatal("Access was not told the action")
	}
	// extension pages: the handler decides; the panel only checks sign-in
	if code, _, _ := e.post(cust, "/x/hm/make", url.Values{"email": {"x@example.test"}, "role": {"customer"}}, "/sites"); code != 422 {
		t.Fatalf("customer made a user: %d", code)
	}
	// CheckCreate refuses with its own words
	form := url.Values{"step": {"plan"}, "domain": {"full.test"}, "primary": {"domain"}, "server": {"nginx"}, "php": {"8.4"},
		"admin_email": {"cust@example.test"}, "title": {"Full"}}
	if code, body, _ := e.post(cust, "/sites/new", form, "/sites/new"); code != 422 || !strings.Contains(body, "package full") {
		t.Fatalf("package limit: %d", code)
	}
	form.Set("domain", "ok.test")
	form.Set("step", "create")
	code, _, h := e.post(cust, "/sites/new", form, "/sites/new")
	if code != 303 {
		t.Fatalf("customer create: %d", code)
	}
	x.mu.Lock()
	if len(x.events) != 1 || !strings.HasPrefix(x.events[0], "created cust@example.test ") {
		t.Fatalf("OnSite: %v", x.events)
	}
	x.mu.Unlock()
	// its own job is visible, another user's job is not
	if code, _, _ := e.get(cust, h.Get("Location")); code != 200 {
		t.Fatalf("own job: %d", code)
	}
	e.srv.owners.set("j00000000000000aa", 1)
	if code, _, _ := e.get(cust, "/sites/job/j00000000000000aa"); code != 404 {
		t.Fatalf("another user's job: %d", code)
	}

	// locking signs the customer out and refuses sign-in; an admin cannot be locked
	id := m[1]
	if code, out, _ := e.post(e.c, "/x/hm/lock", url.Values{"id": {"1"}, "on": {"1"}}, "/x/hm/hello"); code != 422 || !strings.Contains(out, "admin") {
		t.Fatalf("admin locked: %d %s", code, out)
	}
	if code, _, _ := e.post(e.c, "/x/hm/lock", url.Values{"id": {id}, "on": {"1"}}, "/x/hm/hello"); code != 200 {
		t.Fatal("lock")
	}
	if code, _, h := e.get(cust, "/sites"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("locked customer still in: %d", code)
	}
	u, _ := e.st.UserByEmail("cust@example.test")
	if !u.Disabled {
		t.Fatal("not disabled")
	}
	// a new password does not lift the lock
	_ = e.st.SetPassword(u.ID, u.PwHash)
	if u, _ = e.st.UserByEmail("cust@example.test"); !u.Disabled {
		t.Fatal("password change lifted the lock")
	}
	if code, _, _ := e.post(e.c, "/x/hm/lock", url.Values{"id": {id}, "on": {"0"}}, "/x/hm/hello"); code != 200 {
		t.Fatal("unlock")
	}
	_, audit, _ := e.get(e.c, "/audit")
	for _, a := range []string{"user.create", "user.lock", "user.unlock"} {
		if !strings.Contains(audit, a) {
			t.Errorf("audit lacks %s", a)
		}
	}
}

// Without an extension, a user that is not an admin can open nothing but its profile, help and an empty list.
func TestNoExtensionDeniesOthers(t *testing.T) {
	e := newEnv(t)
	e.withAgent()
	e.setupAdmin(e.c, "admin@example.test")
	if code, _, _ := e.get(e.c, "/x/hm/hello"); code != 404 {
		t.Fatalf("extension path without an extension: %d", code)
	}
	adm, _ := e.st.UserByEmail("admin@example.test")
	u, err := e.st.CreateUser(adm.OrgID, "dev@example.test", "x", "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(adm.OrgID, "boss@example.test", "x", "admin"); err == nil {
		t.Fatal("CreateUser made an admin")
	}
	if err := e.st.SetUserLocked(adm.ID, true, e.clock); err == nil {
		t.Fatal("admin locked")
	}
	if err := e.st.DeleteUser(adm.ID); err == nil {
		t.Fatal("admin deleted")
	}
	link, err := AdminRotate(e.st, "dev@example.test", e.ts.URL, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	c := e.client()
	e.signInNew(c, "dev@example.test", strings.TrimPrefix(link, e.ts.URL))
	code, body, _ := e.get(c, "/sites")
	if code != 200 || strings.Contains(body, "voltix.id") || strings.Contains(body, `href="/sites/new"`) {
		t.Fatalf("list without an extension: %d", code)
	}
	for _, p := range []string{"/sites/voltix-id", "/sites/new", "/files/voltix-id", "/settings", "/audit"} {
		if code, _, _ := e.get(c, p); code != 404 {
			t.Errorf("%s without an extension: %d", p, code)
		}
	}
	// a lock written without ending the sessions (by another process) still signs the user out
	e.st.DB.Exec(`INSERT INTO user_locks(user_id,at) VALUES(?,0)`, u.ID)
	if code, _, h := e.get(c, "/sites"); code != 303 || h.Get("Location") != "/login" {
		t.Fatalf("session of a locked user: %d", code)
	}
	if err := e.st.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UserByEmail("dev@example.test"); err == nil {
		t.Fatal("user not deleted")
	}
}

func TestSetExtensionsRefusesBadRoutes(t *testing.T) {
	e := newEnv(t)
	for _, x := range []ext.Extensions{
		{Name: "Bad Name"},
		{Routes: []ext.Route{{Pattern: "GET /a", Handler: func(http.ResponseWriter, *http.Request) {}}}},
		{Name: "hm", Routes: []ext.Route{{Pattern: "DELETE /a", Handler: func(http.ResponseWriter, *http.Request) {}}}},
		{Name: "hm", Routes: []ext.Route{{Pattern: "GET a", Handler: func(http.ResponseWriter, *http.Request) {}}}},
		{Name: "hm", Start: func(ext.Host) error { return errors.New("no licence store") }},
	} {
		if err := e.srv.SetExtensions(x); err == nil {
			t.Errorf("accepted %+v", x)
		}
	}
}
