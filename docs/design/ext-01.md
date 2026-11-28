# Extension points (EXT-01)

Status: LIVE 2026-11-28 19:05 WIB (v1.53.0, installed from Settings → Update). Roadmap slot 15l.

## Goal
A program built on the panel can let people other than the server's admins sign in and use only the websites it
allows them, and can add its own pages, without a fork and without weakening the plain panel. The plain panel
behaves exactly as before: every hook is optional and nothing in the panel depends on an extension.

## How
- `panel/app`: the `nostek-panel` command as a library. `cmd/nostek-panel` is `app.Main(version, ext.Extensions{})`;
  another Go program can import the module and call `app.Main` with its own extensions.
- `panel/ext`: the hooks (all optional):
  - `Access(user, {site, page, action})`: may this user, who is not an admin, make this request on this website?
  - `CheckCreate(user, {domain, app, engine})`: may this user make this website? The error text shows on the form.
  - `OnSite(event, user, site)`: a website was made (its create job started) or deleted.
  - `Start(host)`, `Routes`, `Menu`: pages under `/x/<name>/…` drawn in the panel layout, and menu entries that
    point there.
  - `Host`: what an extension may ask of the panel: the signed-in user, the CSRF token, draw a page, write the audit
    log, make a user (random password shown once through a 15-minute link; two-factor login is set up at the first
    sign-in), lock, unlock or delete a user, list websites, start or stop one, move one to the trash.

## What the panel enforces itself
- **Deny by default:** a user whose role is not `admin` may open only its profile, help, signing out, the website
  list (filtered by `Access`), its own jobs, `/sites/new` when `CheckCreate` is set, the extension's pages (whose
  handlers decide), and website pages `Access` allows. Everything else answers 404, so a page added later stays
  admin-only until it is listed.
- Admin-only whatever `Access` says (`ext.AdminOnly`): resource limits, database access from outside (server
  firewall), wildcard certificates (the server's DNS account), staging copy and push (a second website), the web
  terminal and the stack view.
- Users that are not admins never get the dashboard (server view), the trash, the audit log, settings, API keys,
  updates or server backups; the menu hides those entries.
- Sign-in, two-factor login, CSRF on every POST and the audit log stay the panel's own. An extension can make,
  lock or delete only users that are not admins, only in the admin's organisation, and only from an admin's request.
- A lock is its own table, so a password change or a good sign-in never lifts it; a locked user is signed out at its
  next request and cannot sign in with a password or a passkey.
- Job pages: a user that is not an admin sees only jobs it started.

## Not in this step
- The website tabs still show the terminal tab when the terminal is on; for a user that is not an admin it answers
  404.
- API keys for users that are not admins.

## Tests
`panel/internal/web/ext_test.go`: a test extension (role `customer` may use one website): the extension's page and
menu (foreign menu paths dropped), CSRF on its POST, no admin made through it, a user made and signed in through the
one-time link and two-factor enrolment; the customer is sent from the dashboard to its websites, sees only its
website and no server menu, gets 404 for every server page, another website, the terminal and the stack view, and
for admin-only actions; an allowed action reaches the agent and `Access` is told the action; `CheckCreate` refuses
with its own words; `OnSite` on create; own and foreign jobs; lock signs out and survives a password change, an
admin cannot be locked; audit. Without an extension a user that is not an admin sees an empty list and gets 404
for everything else; a lock written without ending sessions still signs out; bad extension names, routes and a
failing `Start` are refused.
