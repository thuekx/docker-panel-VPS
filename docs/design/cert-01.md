# Own and wildcard certificates (CERT-01)

Status: BUILT v1.35.0, 2026-11-26. Roadmap slot 14c (RAYX-1 = A). Decision CERT-1 = ClouDNS (the DNS of the domains is kept
there).

## Goal
Today every website name gets its own Let's Encrypt certificate through the HTTP challenge, which needs the name
to point to this server. Two cases are not covered:
1. **An own certificate**, for example a Cloudflare origin certificate behind the Cloudflare proxy, a certificate
   bought elsewhere, or one from a company CA.
2. **A wildcard certificate** (`*.example.com`) for many names at once, or for names that cannot pass the HTTP
   challenge (the edge is behind a proxy that does not pass `/.well-known/acme-challenge/`). Let's Encrypt gives a
   wildcard only through the DNS challenge, so the edge needs an API token of the DNS provider.

## Own certificate (per website)
- Domains page, new **Certificate** card: shows what is used now (automatic Let's Encrypt, or the own certificate
  with its names, issuer, expiry and whether a public CA trusts it).
- Upload: the certificate (PEM, the chain after it) and its private key (PEM), as files or pasted. The agent checks:
  it parses; the key belongs to the certificate; it is valid now and for at least one more day; it covers the
  website's main name (other names of the website it does not cover keep their Let's Encrypt certificate). A
  certificate no public CA trusts is accepted with a warning (Cloudflare origin certificates are such).
- Kept in the website folder (`tls/cert.pem` 0644, `tls/key.pem` 0600), so it moves with backups, the trash and a
  server backup. The agent copies both to `<data>/edge/dynamic/certs/` (key 0600, uid 65532, the edge's user) and
  writes `cert-<id>.yml` with `tls.certificates`; the edge's file provider picks it up without a restart. The edge
  does not ask Let's Encrypt for a name a stored certificate covers.
- Remove: the files go, the website is back on Let's Encrypt (issued within a minute when its DNS points here).
- The private key never leaves the agent: not in an answer, a page, the audit log or a log line.
- Expiry: the Domains card shows the days left; the alerts (OPS-01) get a kind "certificate", sent once per
  certificate 14 days before its end (looked at once an hour).

## Wildcard through the DNS challenge (CERT-1 = ClouDNS)
- A second resolver `dns` on the edge (lego DNS-01, provider `cloudns`, storage `/acme/acme-dns.json`). The API
  sub-user's id and password are two files in `<data>/edge/dns/` (0600, uid 65532, mounted read-only at `/dns`);
  lego reads them through `CLOUDNS_SUB_AUTH_ID_FILE` and `CLOUDNS_AUTH_PASSWORD_FILE`. They are never in an
  environment value, the compose file, the panel's database, an answer or a log. Without the files only this
  resolver fails; Let's Encrypt over HTTP goes on (checked with the pinned Traefik image).
- Settings → Wildcard certificates (ClouDNS): sub-auth-id and password. The agent first logs in at ClouDNS
  (`/dns/login.json`, the password in the POST body), then writes the files and restarts the edge two seconds after
  answering, so the answer still reaches the browser through the edge; a failed restart is shown on the page.
  Removal is refused while a website uses a wildcard.
- Domains page: the zone choice lists the main name and the zones above it (at least two labels); a website on its
  preview address has none. The agent checks the zone at ClouDNS (`/dns/get-zone-info.json`), then writes the
  website's routes (job `domains`, step `routes`): routers of names the wildcard covers (the zone itself, or one label
  under it) use resolver `dns` with `tls.domains` = zone + `*.zone`; the other names keep resolver `le`. Routers over
  several names (protected folders, IP rules) use the wildcard while it is on. Native websites get the same in their
  file-provider YAML. If the main name later leaves the zone, the wildcard is unused until it is turned off.
- The sub-user is not part of a server backup; it is set again on a new server. Other DNS providers can follow with
  their own pair of files.

## Tests
Agent: certificate checks (key mismatch, expired, not yet valid, not covering the main name, private CA = warning,
chain order, size limits), files and modes, dynamic file, removal, sync after a trash restore and a server restore,
the key in no answer. Panel: card in both languages, upload as file and as text, remove, audit without the key.
Wildcard: sub-user refused (ClouDNS's words, no password in the error), files and modes, edge restart, zone choice
and refusal of foreign zones, compose labels and native YAML, one-level coverage, removal refused while in use, no
password in an answer, the audit log or compose.yml; panel pages in both languages, CSRF, audit without the password,
the certificate alert once per certificate.
