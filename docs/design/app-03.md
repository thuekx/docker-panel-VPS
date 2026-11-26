# APP-03 — Ready-app catalogue

Status: wave 1 BUILT 2026-11-14 (v1.17.0): Uptime Kuma, n8n. Wave 2 BUILT 2026-11-27 (v1.48.0): Ghost with its own
MySQL 8. Roadmap order (2026-11-12): every stack first
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
- **First account**: both apps let the first visitor create the admin account. The job page says to
  open the app at once. Known limit, follow-up candidate: keep a new ready app behind the panel login until its first
  account exists.
- n8n gets its public address (`N8N_HOST`, `WEBHOOK_URL`, `N8N_EDITOR_BASE_URL`), `N8N_PROXY_HOPS=1` (behind the edge),
  time zone Asia/Jakarta, diagnostics and version checks off.

## Wave 2: Ghost with its own MySQL 8 (v1.48.0)

Ghost supports only MySQL 8 in production (not MariaDB, not SQLite). Each Ghost website brings its own MySQL 8
container in its stack, like the MariaDB of a WordPress website. MySQL is not offered to any other website. Decision GHOST-1 = A (2026-11-27).

| App | Image | Port | Data folder | Database |
|---|---|---|---|---|
| Ghost | `ghost:6.79.0-alpine` | 2368 | `/var/lib/ghost/content` | `mysql:8.4.13` (LTS), database and user `app` |

- **Catalogue**: `catalog.ReadyApp.DB = "mysql"`. The agent sets `db_engine: mysql` at create time (`dbEngine`).
  The form cannot choose it: `spec` still accepts only `mariadb` or `postgres` as a choice, and a ready app takes no
  database option.
- **The db service**: `mysql:8.4.13` by digest, `MYSQL_PASSWORD_FILE` from the website's secret, a random root
  password, `./db` on `/var/lib/mysql`, a `mysqladmin ping` health check, the private network `back` only,
  `mem_limit` 768m.
- **The app service**: `user: 33:33`. Its entry point reads `database__connection__password` from
  `/run/secrets/db_password` as it starts, then runs the image's own `docker-entrypoint.sh`. The password is in no
  compose file and no `docker inspect` output. It also sets `url=https://<canonical name>`, `NODE_ENV=production`
  and turns update checks off. It starts once the database is healthy, and its networks are `back`, `nostek-edge`
  and `out`.
- **Database tools**: the database page, the SQL console, export and import, backups and restore, and the table
  check run on the same code as MariaDB. The client snippet uses `mysql`, `mysqldump` or `mysqlcheck` when the
  `mariadb*` name is missing in the container. The db service also carries `MARIADB_USER` for these tools. The page
  shows the version as "MySQL 8.4.x".
- **Not offered**: access to the database from outside (DB-02). The agent refuses it and the panel hides the card.
  Ghost's database stays on the private network.
- **First account**: as in wave 1, the first visitor of `/ghost` creates the admin account; the job page says so.
- **Mail**: Ghost sends staff invitations and member sign-ins by e-mail. The panel has no mail server (MAIL-1), so
  set SMTP in Ghost's own settings or `mail__*` later. A single admin works without mail.

## Tests

Agent unit tests (compose per app, refusals, backup stops the app, logs), panel tests (form group, approve card,
Overview, job page, list, both languages), Ghost: compose (MySQL service, password from the secret file and never in the compose file, networks), refusals
(database option, MySQL chosen for another app), approve card names MySQL 8; harness on real Docker (both apps healthy as uid 33 with their data in the
website folder, no published port, backup with the app stopped and back, refusal, catalogue), 3 new mutants.
