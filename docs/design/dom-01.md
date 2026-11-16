# Domains after create (DOM-01)

Status: [BUILT] v1.22.0, 2026-11-17. Roadmap slot 12a, after MIG-01. Like the cPanel pages Domains, Aliases,
Redirects and "Track DNS", for one website at a time. DNS itself stays at the domain's provider (no Zone Editor).

## Why now
MIG-01 moves a site in on its preview name while the domain still points to the old host. Until now a name was
served only when DNS pointed here at create time, so a moved site could never switch to its domain. DOM-01 closes
that: after DNS is switched, one press of **Check DNS again** serves the domain and moves WordPress's addresses.

## What the Domains tab shows
Every name of the website (domain, www, extra names, preview) with:
- **DNS now:** the A and AAAA answers and a verdict: points here, points elsewhere, partly here (some A records are
  other servers), no record, lookup failed. Each verdict says what to do, with this server's IPv4 address.
- **AAAA warning:** this server has no IPv6 yet. An AAAA record elsewhere sends IPv6 visitors to the other server,
  and Let's Encrypt prefers IPv6, so the certificate can fail. The page says to remove it.
- **Served or not**, and the main address (the domain once it is served, else the preview name).
- **Certificate** of every served name, read from the edge with the name as SNI: valid until (issuer), being
  issued (the edge still shows its default certificate), or not valid with the error.

## Changes (each one a job)
| Change | Rule |
|---|---|
| Add a name | a valid lower-case host name, not under the preview zone, not used by any website or the panel, at most 10 extra names. Served at once when its A record points here, else listed and served after a later check. |
| Remove a name | extra names and www only; the domain and the preview name stay. |
| Check DNS again | every name not served yet that now points here is served. It never stops serving a name (a DNS hiccup must not take a website down); removing does that. Nothing new = no job. |
| Add a redirect | from a path (`/[A-Za-z0-9._~/-]`, no `//`, no `..`, never `/.well-known`) to a path of the website or an http(s) address; 301 or 302; at most 50; a path once. |
| Remove a redirect | by its path. |

The job writes the edge configuration and site.json: Docker websites get new Traefik labels (`up -d`, the web
container is recreated, a few seconds), native websites get their Nginx server and edge file (Nginx reloaded, PHP
untouched). When the **main address moves** (the domain is served for the first time) on WordPress, the job also
rewrites the addresses with WP-CLI (same commands as MIG-01: scheme-less, JSON-escaped, http to https, home and
siteurl) and checks home. If that fails, the previous addresses and routes come back.

## Redirects at the edge
Redirects are Traefik `redirectRegex` middlewares on the main address's router (every other name first redirects
to the main address). The regex matches the path with or without a closing slash and any query:
`^https?://[^/]+/old/page[.]html/?([?].*)?$`; the replacement keeps the query (`${1}`) unless the target has its
own. Only characters that can sit as is in a compose label and in a YAML string are accepted (no quotes,
backslashes, `$`, braces or spaces). Docker labels double `$` for compose. Traefik answers 301 for permanent and
302 for temporary GET requests.

## Not in this release
Changing which name is the main domain, wildcard names, IPv6 serving, DNS zone editing, redirects by regex or
per host.

## Tests
- Agent unit tests: DNS verdicts (fake resolver with NXDOMAIN), add/remove/refresh rules, address move with
  WP-CLI and its roll-back, redirect validation, labels and native edge output, native Nginx reload and roll-back.
- Panel tests: page in both languages for every job state, every form, audit.
- `packages/site-01/tests/domains.sh`: real Docker and a real Traefik v3 edge. A WordPress website made while its
  domain points elsewhere; redirects 301/302 through the edge; an extra name served and redirected to the main
  address; DNS switched in /etc/hosts and one check moves the site to its domain (links in posts too); removing.
