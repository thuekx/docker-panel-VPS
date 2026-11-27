# Static releases, headers, folder redirects, allow-list, visitor address (SITE-03)

Status: LIVE v1.31.0 2026-11-26 16:23 WIB (built 2026-11-23). Roadmap slot 13a, after OPS-01 wave 2 and before INST-01. Asked for in
CL-NOSTEK-20261123-02 and CL-NDEV-20261123-01 (status page, nostek.dev and kb.nostek.dev move to this server from
December). Every part works for every app and both engines unless it says otherwise.

## Releases of a static website
- The Overview of a static website gets a **Releases** card: upload a .zip of the built output (the `dist` or
  `build` folder of React, Vue, Svelte, Astro), up to 512 MB. The zip needs an `index.html` at its top or inside its
  one top folder (`dist/index.html`), which is then stripped. `__MACOSX` and `.DS_Store` are ignored.
- The zip is streamed from the browser through the panel to the agent (`PUT /v1/sites/{id}/releases?name=`), never
  buffered by the panel. The agent writes it to `<site>/static/upload-*.zip` (0600, stops 1 GB before the disk is
  full) and unpacks it with the File Manager's unzip code: no symbolic links, no name that leaves the folder, at most
  20 000 entries and 2 GB unpacked, files owned by the website's file user.
- Swap: every entry of the live folder `wp/` moves into `<site>/static/releases/<live>/`, then every entry of the new
  release moves into `wp/`. Renames on one disk, so visitors never see a half-written site; `wp/` itself stays, so
  the web server's mount and the native engine's root keep working without a restart. A failed step moves back what
  was moved. The website's own `.nostek-errors` (error pages) and `.well-known` stay in `wp/` across every swap.
- Kept: the live release and the two before it (like Node.js deploys, `KeepReleases` = 3); older folders are removed.
  What was live before the first upload is kept as release 0, so it can be put back too. History (30 entries) in
  `<site>/static/history.json` (0600): number, time, file name, files, size, result live / ok / removed, rollback.
- **Make live** on a kept release swaps it back in the same way (`POST /v1/sites/{id}/releases/{n}/rollback`).
- Both take the website's change lock (busy while another change runs). The audit log names the release number, the
  file name and the number of files.
- Releases sit outside `wp/`, so a website backup holds the live files only.

## Folder redirects
- The Domains page's redirect form gets **Whole folder**: `/kb` to `https://kb.example.com` sends `/kb/a/b?x=1` to
  `https://kb.example.com/a/b?x=1` and `/kb` itself to the target; `/kbx` is not part of the folder.
- Edge: `redirectregex` `^https?://<host>/kb(/[^?]*)?([?].*)?$` → `<target>${1}${2}`.
- Refused: a folder of `/`, a target with its own `?` or `#`, and a local target inside the folder itself (a loop).

## Response headers
- The Performance page gets **Response headers**: name and value per folder. Folder `/` is every answer; a folder
  such as `/assets` covers `/assets` and everything under it. An empty value removes the header from the answers
  (for example `X-Powered-By`). Setting a header again replaces its value.
- At most 20 headers on at most 10 folders. Names: letters, digits and `-`, shown as browsers write them; the edge or
  the browser needs these as they are, so they are refused: Content-Length, Transfer-Encoding, Connection,
  Keep-Alive, Upgrade, Host, Set-Cookie, Location, Content-Encoding, Trailer, TE, WWW-Authenticate,
  Proxy-Authenticate, Date, Alt-Svc. Values: printable ASCII without `"` `\` `` ` `` or `$`, up to 4096 characters
  (room for a CSP with some 60 hashes and `'wasm-unsafe-eval'`).
- Edge: one Traefik `headers` middleware per folder (`customResponseHeaders`). The middleware for `/` rides on every
  router that serves the website (the main one, protected folders, the browser-cache router). A folder with headers
  gets its own router at priority 6000 + the folder's length, above the browser-cache router (5000) and below
  protected folders (10000+); with Optimize Website's browser cache on, a second router (6300 + length) keeps the
  cache header on the folder's static files.
- Order: of two middlewares setting the same header, the one listed first wins, so a router lists the folder's
  headers, then the browser cache, then the headers for `/`. A folder's own Cache-Control (`immutable`) wins over
  the browser cache days, which win over a Cache-Control for `/`.
- A protected folder keeps only the headers for `/`. The audit log names the folder and header, never the value.
- Shown with Optimize Website on the Performance page; the change runs as an "optimize" job.

## Allow only these addresses
- The Security page gets **Allow only these addresses**: when the list holds an address or network (same rules as the
  IP blocker, at most 200), only those reach the website on all its names; everyone else gets 403 from the edge.
  Removing the last entry opens the website again. The first entry asks for a confirmation.
- Edge: a router at priority 30001: `Host(...) && !PathPrefix(/.well-known/) && !(ClientIP(a) || ClientIP(b) …)` to
  the deny middleware. Certificates are issued on port 80 by the edge itself and are not affected.
- `ClientIP` matches the address the edge was reached from, never a header, so this is the list for "only the proxy
  or CDN in front may reach this website" (the proxy's own addresses), or for a few offices.

## The visitor's address behind a proxy in front
Measured on Traefik v3.7.18 (the edge's image) before building:
- Without trust, the edge drops a visitor's own `X-Forwarded-For`: the access log, routers and the website see the
  address the edge was reached from. A made-up header changes nothing.
- With `--entryPoints.websecure.forwardedHeaders.trustedIPs`, requests from those addresses keep their
  `X-Forwarded-For`; the access log's ClientHost becomes that list (`"visitor-claimed, real"`); `ClientIP` still
  matches only the connecting address.

So:
- Server-wide setting, made by the update script (the edge's start options are fixed when it starts):
  `panel-01-core.sh UPDATE … --trusted-proxies "203.0.113.0/24,2001:db8::/32"` (`none` clears it). With the running
  commit it changes only this setting (edge and firewall recreated, the previous setting put back on failure); with a
  new commit it goes with the update. Kept in the panel-01 state for later updates and shown by VERIFY. `0.0.0.0/0`
  and `::/0` are refused, at most 50 entries. Core `.env`: `EDGE_TRUSTED_PROXIES`, default `127.0.0.1/32` (nobody).
- Statistics and login attempts read the right-most entry of ClientHost: the address the trusted proxy was reached
  from, which a visitor cannot make up. One proxy layer is supported.
- The firewall (WAF) reads `X-Forwarded-For` from the right and skips the trusted proxies (`EDGE_TRUSTED_PROXIES` in
  its environment); without any, the right-most entry is the visitor, as before.
- IP blocker: every blocked address also matches as the right-most `X-Forwarded-For` entry (`HeaderRegexp`), which
  only a trusted proxy can send. IPv4 networks become octet patterns; IPv6 networks match by `ClientIP` only.
- The panel itself keeps reading the right-most entry (the edge's own), so its sign-in limits never trust a header.
- The allow-list stays on `ClientIP` on purpose (see above).

## Result toasts (panel, all pages)
- Asked for before merging v1.31.0: the result banner at the top of a page (saved, deleted, restarted, an error) is not
  seen once the page is scrolled down. Each result banner (`data-toast`) is also shown as a toast at the bottom right:
  ✅ with a green edge for done, ⚠️ amber or red for a warning or error, the banner's own text, a close button.
- It leaves after 5 seconds (an error after 9), waits while the pointer or the keyboard focus is on it, stacks (at
  most 3), and slides in only without reduced motion. The banner at the top stays and is the one screen readers
  announce, so the toast box has no live region. Without JavaScript nothing changes.

## Tests
- Agent: `release_test.go` (refused zips, top folder stripped, error pages kept, keep 3, rollback, removed release
  refused, 0600 history, only static websites), `headers_test.go` (validation, labels and native YAML, order,
  allow-list router, removal leaves nothing, `X-Forwarded-For` patterns hold to the right-most entry), folder
  redirect tests in `domains_test.go`, API route tests.
- Panel: `toast_test.go` (toast box and close label in both languages, result banners marked, none without a result); Releases card and upload (token first, only .zip, path stripped from a Windows file name), rollback, audit;
  folder redirect checkbox; headers card (value not in the audit log); allow-list form; WAF client address behind a
  trusted proxy.
- Harness with the real edge: `domains.sh` (folder redirect through Traefik), `security.sh` (releases through the edge,
  headers and their order, allow-list, blocker by `X-Forwarded-For` from a trusted source), `panel-01/harness.sh`
  (`--trusted-proxies`: refused values, set on the running release, edge and firewall take it, cleared).
