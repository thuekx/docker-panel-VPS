# APP-03 — Ready-app catalogue

Status: wave 1 BUILT 2026-11-14 (v1.17.0): Uptime Kuma, n8n. Roadmap order (owner 2026-11-12): every stack first
(APP-01, APP-02), then IMG-01, then APP-03. Examples named in app-01.md: Ghost, n8n, Uptime Kuma.

## Wave 1: apps that keep their data in one folder

| App | Image (pinned by digest in `agent/internal/catalog`) | Port | Data folder | Database |
|---|---|---|---|---|
| Uptime Kuma | `louislam/uptime-kuma:2.5.11-slim-rootless` | 3001 | `/app/data` | SQLite (`UPTIME_KUMA_DB_TYPE=sqlite`, no setup page) |
| n8n | `n8nio/n8n:2.47.7` (the `stable` line) | 5678 | `/data` (`N8N_USER_FOLDER`) | SQLite; n8n makes its encryption key in `.n8n/config` (0600) |

- **One container** (`app`), the upstream image as it is, `user: 33:33` (www-data), `./wp` mounted on the data folder,
  `mem_limit` 768m, the shared `x-site` limits (no-new-privileges, dropped capabilities, pids, log size), healthcheck
  over HTTP on its port, networks `nostek-edge` + `out` (monitors and workflows need the internet), no published port.
- **Nothing to choose** but the domain: `spec` refuses a web server, PHP, version, database, repository or e-mail.
- **Data in the website folder**, owned by www-data: the File Manager shows it, BAK-01 backups and restores carry it,
  IMG-01 Update image moves the app to newer pins. A backup stops the app while the files are copied (SQLite must not
  be copied while it writes) and starts it again: a few seconds offline, also for the nightly backup.
- **First account**: both apps let the first visitor create the admin/owner account. The job page tells the owner to
  open the app at once. Known limit, follow-up candidate: keep a new ready app behind the panel login until its first
  account exists.
- n8n gets its public address (`N8N_HOST`, `WEBHOOK_URL`, `N8N_EDITOR_BASE_URL`), `N8N_PROXY_HOPS=1` (behind the edge),
  time zone Asia/Jakarta, diagnostics and version checks off.

## Wave 2 (not built): apps that need another database

**Ghost** supports only MySQL 8 in production (not MariaDB): it needs a third database engine (image, Database page,
dump/import, backups, restore) before it can join. Decide with the owner which apps come next.

## Tests

Agent unit tests (compose per app, refusals, backup stops the app, logs), panel tests (form group, approve card,
Overview, job page, list, both languages), harness on real Docker (both apps healthy as uid 33 with their data in the
website folder, no published port, backup with the app stopped and back, refusal, catalogue), 3 new mutants.
