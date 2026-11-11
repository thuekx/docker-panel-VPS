# SITE-01 — one-click WordPress, Apache or Nginx per site (design draft)

Status: draft for the owner's decision. Builds on PANEL-01 (done 2026-11-11) and the architecture in
[architecture.md](architecture.md): one stack per site, MariaDB per site, per-site network, edge proxy in front.
Roadmap slot SITE-01, owner thread "Rancang panel Docker untuk VPS fresh". Also carries THEME-1 (light/dark setting)
and two PANEL-01 follow-ups (APPLY summary text, backup status on the dashboard).

## What the owner gets

1. Panel → **Websites → Add website**: domain, web server (**Apache** or **Nginx**), PHP version, admin e-mail.
2. The panel checks that the domain points to this server, shows an **Approve card** (what is created, disk/RAM
   estimate, how to undo) and creates the site after approval.
3. Progress is shown live; when done: HTTPS certificate valid, WordPress installed, admin password shown **once**
   through a one-time link (same pattern as ADMIN-ROTATE-1).
4. Website list: status (never green when unknown), domain, web server, PHP, disk use, last backup; actions
   start/stop/restart, logs, delete (moved to trash for 7 days, then removed).

## One site = one stack

| Container | Apache site | Nginx site |
|---|---|---|
| web | `wordpress:<ver>-php8.3-apache` | `nginx` (pinned) serving static files, PHP to `php` |
| php | — | `wordpress:<ver>-php8.3-fpm` |
| db | `mariadb:11.x` (pinned), own volume, own password | same |

- Own Docker network per site; only `web` also joins `nostek-edge`. Sites cannot reach each other or the panel.
- Files: `/srv/nostek/sites/<site-id>/wp` (WordPress) and `/srv/nostek/sites/<site-id>/db` (database); inside the
  nightly backup. A database dump is taken before every nightly backup so restores are consistent.
- Limits per site (changeable later): memory 512 MB web/php, 512 MB db; upload size 128 MB.
- `www.` and the bare domain both answer; the other one redirects to the one the owner picks.
- WordPress install via WP-CLI inside the container (no installer page left open on the internet).

## Decision needed: how the panel is allowed to change Docker (SITE-DOCKER-1)

Today the panel can only **read** Docker (socket proxy refuses all writes). Creating sites needs writes.
Write access to Docker is equal to root on the server, so this is the main security choice.

| Option | How | Plus | Minus |
|---|---|---|---|
| **A (recommended)** | Small **nostek-agent** on the host, separate from the panel. It accepts only fixed actions (create site from our template, start, stop, restart, logs, delete to trash) with checked inputs (domain, server type, PHP version from a list). The panel talks to it over an internal socket; every call is logged in the audit log. | A bug or break-in in the web panel cannot run arbitrary containers or mount the host disk. Clear audit trail. | One more component to build and test. |
| B | Give the panel write access through the socket proxy (`POST=1`, containers, networks, volumes). | Fastest to build. | Anyone who takes over the panel effectively has root on the server. Not recommended for a panel on the internet. |
| C | No one-click: the panel produces a site package, the owner runs it over SSH like HOST-0x. | Panel stays read-only. | Not one-click; does not meet the goal. |

## Other choices (recommendation applies unless the owner says otherwise)

| Code | Choice | Recommendation |
|---|---|---|
| SITE-PHP-1 | PHP versions offered | 8.3 (default) and 8.2; more later. |
| SITE-WPADMIN-1 | First WordPress admin | Created by WP-CLI with a random password shown once via one-time link. |
| SITE-DELETE-1 | Deleting a site | Stop + move to trash, removed after 7 days; restore from trash in the panel. |
| THEME-1 | Light/dark | Light / Dark / Follow system; per user, in the header and in Settings. |

## Owner input needed

- The first 2-3 domains, and for each: web server (Apache or Nginx) and whether `www.` or the bare domain is
  the main address. DNS A records must point to the server before creating.

## Delivery

- `nostek-agent` (Go, same repo), package `packages/site-01/` installs/updates the agent and the new panel version.
- Harness on real Docker: create Apache site, create Nginx site, both reachable through the edge with WordPress
  answering, isolation test (site A cannot reach site B's database), delete to trash and restore, agent refuses
  anything outside its fixed actions.
- Mutants on the agent's input checks.
