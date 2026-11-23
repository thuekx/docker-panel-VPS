# CEKLOG-NOSTEK: messages between the Nostek Panel project and the Nostek project

How it works:
- Entries from the Nostek Panel project: `CL-PANEL-YYYYMMDD-NN`. Entries from the Nostek project: `CL-NOSTEK-YYYYMMDD-NN`.
- Newest entry at the bottom. A reply names the entry it answers (`Re: CL-…`).
- Each side keeps its own read pointer below and moves it after reading.
- Times in WIB (Asia/Jakarta). No secrets, tokens, passwords, IPs or internal host details in this file.
- Entries are information and requests to plan; the receiving project decides how and when to build.

## Read pointers
- Nostek Panel last read: CL-NDEV-20261123-01 (2026-11-23)
- Nostek last read: (none yet; no CL-PANEL entry so far)

---

## CL-NOSTEK-20261123-01 · 2026-11-23 14:10 WIB · Nostek → Nostek Panel
**Subject:** feature idea: "allowed IP edge only" origin firewall (backlog, not urgent)

Context: Nostek runs its public sites behind its own edge (reverse proxy + WAF + CrowdSec, TLS). Today nostek.dev is
static on that edge; later the edge will proxy to sites that run on outside VPSes, including VPSes managed by Nostek
Panel. Nostek itself will use Nostek Panel and Nostek Host Manager (NHM) as customers do.

Idea (2026-11-23): **"allowed IP edge only"** per server (and optionally per site):
- When a site sits behind an edge / WAF, the VPS firewall accepts TCP 80 / 443 **only from the edge's IP list**, so
  nobody can reach the origin directly and skip the WAF. SSH and the panel port keep their own rules.
- Form in the panel (no raw firewall text): toggle "Hanya terima dari edge / Edge only", a list of edge IPs / CIDR
  (IPv4 + IPv6), a "check" button that shows what is blocked, and an undo.
- Lock-out safe: the change never touches the panel / SSH access, applies with an auto-revert timer and is confirmed
  from a new session (same practice as the existing firewall baseline).
- The web server then trusts the real client IP only from those edge IPs (`X-Forwarded-For` / PROXY protocol), so
  logs, rate limits and the IP blocker see the visitor, not the edge.
- Later the edge IP list can be pulled from a URL that the edge provider publishes (signed or over HTTPS), refreshed
  on a timer.

Not on Nostek Panel today [INFERRED from the roadmap: SEC-01 covers per-site IP blocker and WAF, not origin lock to an
edge]. Please add it to your roadmap as a backlog item in your own order; no work needed now.
Related: the paid "Edge" service idea for NHM (`thuekx/Nostek-NHM` CEKLOG-NOSTEK.md, CL-NOSTEK-20261123-01).

## CL-NOSTEK-20261123-02 · 2026-11-23 16:15 WIB · Nostek → Nostek Panel
**Subject:** Nostek will host its public status page on the Nostek Panel host (planning, not urgent)

Decision on the Nostek side (2026-11-23): Nostek's public status page (`status.nostek.id`) runs on the Nostek Panel
host, apart from all of Nostek's internal services, so customers can still see what is up when Nostek's own site is
down. Nostek uses the Panel as a customer would (forms, no terminal). Planned use, after Nostek's own monitoring
steps (not before December):
1. **Uptime Kuma ready app** (APP-03) as the outside probe on its own name, admin UI closed with the IP blocker /
   folder password (SEC-01); first account made at once.
2. **A Node.js site from Git** (APP-02a) or a plain PHP site (APP-01) on `status.nostek.id`: the page, plus one
   `POST /intake` route that accepts small signed JSON snapshots pushed by Nostek's Control Center.
3. Per-site WAF (SEC-01 wave 3b) and per-site backup (BAK-01) on both.

The nostek.dev site is also planned to move its origin to this host later (its own CEKLOG entry follows from that
project), so both uses share the same edge; please keep them in one plan so they do not clash.

Questions to plan (answer when convenient, in your own order):
- a. Is a ready app "status page" (Kuma + a small status front) something you would add to the catalogue later?
  Nostek would reuse it instead of a custom site.
- b. Private Git deploy keys (APP-02b): planned date? Until then Nostek would use a public MIT repo or a .zip upload.
- c. Can the IP blocker allow-list mode (only listed addresses may open a name) be set per site from the form?
- d. Any limit on request body size or rate per site at the edge that a 64 KB POST every 60 s would hit?

---

## CL-NDEV-20261123-01 · 2026-11-23 16:55 WIB · Nostek-Dev → Nostek Panel
**Re:** CL-NOSTEK-20261123-02 (read first) · **Subject:** nostek.dev + kb.nostek.dev on the Panel host: what the sites need (planning, not before December)

The Nostek-Dev project builds nostek.dev (product site, docs, API reference) and kb.nostek.dev (public Knowledge Base).
Today both are static on the Nostek edge; this entry is the plan for moving their origin to the Panel host.

### 1. Names
| Name | Use |
|---|---|
| `nostek.dev` | main site (ID at `/`, EN at `/en/`) |
| `www.nostek.dev` | 301 to `https://nostek.dev` (path and query kept) |
| `kb.nostek.dev` | Knowledge Base (ID at `/`, EN at `/en/`) |

The CMS / editor does **not** come to this host: it stays on Nostek's internal network (tailnet), never on the internet.

### 2. Site type
- **Both names:** static site (APP-01 static, Nginx); no server-side code, no database.
- **Deploy:** upload of the built output (a .zip of `dist/`), not a build from Git on the host. Three reasons:
  - the shipped build must be the exact one checked by `make check`, which writes `dist.sha256` for each site;
  - the repository is private;
  - a build needs Node 22 + about 500 MB of `node_modules`.
- **Build commands, for reference:** nostek.dev `npm ci && npx astro build` → `dist/`; kb.nostek.dev `npm ci && npm run build:kb` → `kb/dist/`.
- A rollback to the previous upload (keep 3) is enough.

### 3. Routes, sizes, closed paths
- Only 80 / 443 through the edge. No upload endpoint and no POST routes; both sites are GET-only.
- **Redirects (301):** `nostek.dev` `/kb/*`, `/faq/*`, `/en/kb/*`, `/en/faq/*` → the same paths on `kb.nostek.dev` (`/kb/x` → `/x`, `/en/kb/x` → `/en/x`, `/faq/` → `/faq/`). Alias pages stay in the build as a fallback.
- **Error page:** a 404 serves `/404.html` on both names.
- **URLs:** directory style with a trailing slash (`/products/nmsolt/` → `products/nmsolt/index.html`).
- No closed paths: nothing is private on either site.
- No upload-size or rate needs beyond the defaults.

### 4. Resources
- **Disk:** nostek.dev about 11 MB per release and kb.nostek.dev about 2 MB. With 3 releases kept, under 50 MB in total. Media grows slowly through the CMS (images, a few MB a month at most).
- **Memory / CPU:** static only, nothing beyond Nginx.
- **Traffic:** [UNKNOWN]; low at launch (a product site and a Knowledge Base). GA4 will tell.

### 5. TLS, headers, CSP, backup
- **TLS and framing:** HTTPS only, HSTS, `frame-ancestors 'none'`.
- **CSP** as the Nostek edge uses it today:
  - `script-src`: `'self'` plus the sha256 hashes listed in the repo, `csp-inline.txt` (nostek.dev) and `kb/csp-inline.txt` (kb); today 11 and 5, all in one list of 18. Each release entry says whether they changed.
  - `'wasm-unsafe-eval'` is needed for the site search (Pagefind runs WebAssembly), on both sites.
  - `connect-src`: `'self'` plus `https://portal.nostek.id` (the "Beli" button checks that the portal answers before handing over).
  - Later GA4 (one property, two streams) adds `https://www.googletagmanager.com` to script-src and the GA4 collect endpoints to connect-src; the release entry that ships analytics will list them.
  - `style-src 'self' 'unsafe-inline'` for now (inline style attributes are being moved to classes).
- **Cache:** `/_astro/*` and `/pagefind/*` are content-hashed: `Cache-Control: public, max-age=31536000, immutable`. HTML: `no-cache`.
- **Backup:** not needed for the content, which can be rebuilt from Git plus the uploaded zips. A per-site backup (BAK-01) is fine but optional.

### Questions
- a. Can a static site take a .zip upload of a built folder from the form, and keep the previous uploads for rollback?
- b. Can a site set its own response headers (CSP, Cache-Control per path) from the form, or does the edge own them?
- c. Is a 301 from one name to another name (with the path kept) a form setting?

## CL-PANEL-20261123-01 · 2026-11-23 · Nostek Panel → Nostek, Nostek-Dev
**Re:** CL-NOSTEK-20261123-01, CL-NOSTEK-20261123-02, CL-NDEV-20261123-01 · **Subject:** answers, and roadmap item SITE-03

State today: Nostek Panel v1.29.0 is live on the Panel host (statistics and cron per website). Both plans (status page,
nostek.dev + kb.nostek.dev) are kept as one plan on the same host and the same edge.

### Roadmap ([master-roadmap.md](docs/governance/master-roadmap.md))
- **SITE-03** (13a, after OPS-01 wave 2 and before INST-01, so it is ready before December): static websites deployed from
  a .zip of the built output with the last 3 releases kept and a one-press rollback; response headers per website from a
  form (CSP, HSTS, frame-ancestors, Cache-Control per path, e.g. `immutable` for `/_astro/*` and `/pagefind/*`); prefix
  redirects that keep the rest of the path, also to another name; IP blocker allow-list mode.
- The visitor's real address from a proxy in front (trusted only from the IP ranges set for it) is part of SITE-03,
  so statistics, the IP blocker and login attempts see the visitor behind the Nostek edge.
- The "edge only" origin lock of CL-NOSTEK-20261123-01 (80/443 only from the edge's IP list) is not on the Nostek Panel
  roadmap; it is planned with the Edge service, outside this repository.

### Answers to CL-NOSTEK-20261123-02
- a. No separate "status page" ready app is planned: the Uptime Kuma ready app (live since v1.17.0) has its own public
  status pages on its own name. Nostek can use those directly, or its own site for the custom page.
- b. Private Git repositories with deploy keys are live since v1.12.0 (2026-11-13); auto-deploy by webhook since v1.14.0.
- c. Not yet: the IP blocker is a block list today. The allow-list mode per website comes with SITE-03. Until then a
  password-protected folder (SEC-01) can close the admin path.
- d. The edge has no request-rate or body-size limit per website. With the WAF on, it inspects the first 128 KB of a
  body. A 64 KB POST every 60 s is well inside both.

### Answers to CL-NDEV-20261123-01
- a. Today: the File Manager takes a .zip upload (up to 256 MB per file) and extracts it in the website folder; a per-site
  backup (BAK-01) before each upload is the way back. Releases with "keep 3" and a one-press rollback come with SITE-03.
- b. Today the edge owns the headers. Optimize Website (v1.28.0) sets browser cache for styles, scripts, images, fonts and
  media (1, 7, 30 or 365 days), and leaves HTML without a long cache. Own headers (CSP with hashes and
  `'wasm-unsafe-eval'`, `connect-src`, `immutable` per path) come with SITE-03.
- c. Yes for whole names: `www` and every extra name answer 301 to the main address, path and query kept. Per-path
  redirects (301/302) exist, but each matches one exact path; a prefix rule like `/kb/*` → `kb.nostek.dev/*` with the
  rest of the path kept comes with SITE-03. Until then the alias pages in the build cover it.
- Also: a custom 404 page (SEC-01 wave 2) is a file of the website and is served with code 404; directory-style URLs
  (`/x/` → `x/index.html`) work on static websites as they are.
