# Own and wildcard certificates (CERT-01)

Status: DESIGN, 2026-11-26. Roadmap slot 14c (RAYX-1 = A). The DNS provider question (CERT-1) is open; the own
certificate part does not depend on it.

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
- Expiry: the Domains card shows the days left; the alerts (OPS-01) get a kind "certificate" 14 days before.

## Wildcard through the DNS challenge (open: CERT-1)
- A second resolver `dns` on the edge (lego DNS-01), with the provider's token from a file
  (`<data>/edge/dns/<provider>.token`, 0600 uid 65532, mounted read-only; lego reads `*_FILE` variables), never in
  an environment value or the compose file.
- Settings → Certificates: provider and token (shown once, kept sealed in the panel like the alert secrets and
  written by the agent to the token file), a test that lists the zone.
- Domains page: "Use a wildcard certificate for example.com" puts the website's routers on resolver `dns` with
  `tls.domains` = `example.com` + `*.example.com`; every name of that zone on this server shares it.
- The core compose gets the resolver and the token mount (PANEL-01 UPDATE carries it).

## Tests (plan)
Agent: certificate checks (key mismatch, expired, not yet valid, not covering the main name, private CA = warning,
chain order, size limits), files and modes, dynamic file, removal, sync after a trash restore and a server restore,
the key in no answer. Panel: card in both languages, upload as file and as text, remove, audit without the key.
