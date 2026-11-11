# Nostek Panel — architecture (draft v1)

Status: [PROPOSED] — chosen direction PANEL-ARCH-1 = C (own MIT panel on Docker), 2026-11-11.

## Building blocks

| Part | Software (open source) | Licence | Role |
|---|---|---|---|
| Edge proxy | Traefik | MIT | Routes each domain to its site, issues and renews HTTPS certificates. |
| Panel | Nostek Panel (this repo) | MIT | Web UI + API; creates, changes and removes site stacks. |
| Docker access | Docker socket proxy | Apache-2.0 | The panel talks to Docker only through an allow-listed API, never the raw socket. |
| Site web server | Official WordPress image, Apache variant **or** Nginx + official WordPress PHP-FPM image | GPL (separate programs) | Chosen per site at create time. |
| Database | MariaDB (one per site) | GPL (separate program) | Isolated per site. |
| DB admin | Adminer or phpMyAdmin, started on demand behind panel login | Apache-2.0 / GPL | Opened from the panel only. |
| File manager | FileBrowser, one scope per site | Apache-2.0 | Limited to the site's own folder. |
| Backups | restic | BSD-2 | Encrypted per-site backups, restore from the panel. |

Third-party software runs as separate container images; none of its code is copied into this repository.

## Rules the design follows

- **Per-site isolation:** each site has its own Docker network, volumes and database; sites cannot reach each other.
- **Console first:** every recurring task is a form or button in the panel; the terminal is only for the one-time install.
- **Approve cards:** every change starts with a plain summary (what, which site, new/changed/deleted, impact, undo).
- **Time zone:** the server and panel show WIB by default; the zone is a per-install setting; storage stays UTC.
- **Security:** two-factor login, HTTPS only, firewall (only 80/443 and the SSH port open), brute-force protection,
  break-glass admin password rotate via one-time link.
- **Multi-tenant ready:** sites belong to an organisation; another organisation's site answers 404.
- **Status honesty:** unknown or stale status is never shown green.

## Site lifecycle (target)

1. Owner fills a form: domain, web server (Apache / Nginx), PHP version, admin e-mail.
2. Approve card shows the summary.
3. Panel creates network, volumes, database, web stack, routing and certificate.
4. Panel shows the site with health, logs, file manager, DB admin, backup and delete actions.
