# PANEL-01 — edge proxy + panel skeleton with 2FA

Status: done 2026-11-11 15:06 WIB (package `packages/panel-01`, app `panel/`, stack `deploy/core/compose.yml`). Stack (PANEL-STACK-1, owner 2026-11-11 12:56 WIB): Go, server-rendered HTML, SQLite; UI animation
with Motion and anime.js (both MIT), vendored and pinned, honouring `prefers-reduced-motion`. Roadmap slot PANEL-01, owner thread "Rancang panel Docker untuk VPS fresh".

## Containers (one Docker Compose project `nostek-core`)

| Container | Image | Exposed | Notes |
|---|---|---|---|
| `edge` | Traefik (pinned version) | 80, 443 on the host | Only container that publishes ports. HTTP→HTTPS redirect, Let's Encrypt (HTTP-01), security headers. |
| `panel` | `nostek/panel` (built from this repo, pinned digest) | none (behind edge) | Web UI + API on `panel.<domain>`. Runs as non-root, read-only root filesystem. |
| `docker-api` | Docker socket proxy | internal network only | Allow-list: containers, images, networks, volumes, exec off by default. The panel never mounts the raw socket. |

Networks: `nostek-edge` (edge ↔ panel ↔ site web containers), `nostek-ctl` (panel ↔ docker-api, internal).
Data: `/srv/nostek/panel` (SQLite database, settings), included in the nightly backup.

## Login and security

- First admin is created by the installer: a one-time setup link (valid 30 min) printed in the terminal; the
  admin sets the password and enrols TOTP 2FA on first visit. No default password exists.
- Passwords: Argon2id. Sessions: HttpOnly + Secure + SameSite=Strict cookies, 12 h idle timeout, CSRF tokens on every form.
- 2FA (TOTP) mandatory for admins; 10 single-use recovery codes shown once.
- Login rate limit per IP and per account; failed logins logged with time in WIB.
- Break-glass admin password rotate (ADMIN-ROTATE-1): command on the host creates a new random password and a
  one-time link; the password is never printed.
- Every change goes through an Approve card (plain summary: what, which site, new/changed/deleted, impact, undo).

## Pages in PANEL-01

1. Login → 2FA → Dashboard: server name, time zone, CPU/RAM/disk, Docker status, last backup (never green when unknown or stale).
2. Settings: panel domain, time zone, admin e-mail, 2FA management.
3. Audit log: who did what, when (WIB).
UI text in locale files (`id`, `en`), parity checked by a test.

## Delivery

- Package `packages/panel-01/` (PREFLIGHT / APPLY / SETUP-LINK / ADMIN-ROTATE / VERIFY / REVERT). The image is built
  on the host from a pinned commit; base and service images are pinned by digest.
- The first-admin link comes from `SETUP-LINK`, run outside tmux and without a log file, so the link is not stored.
- Harness (CI, real Docker): builds the image, runs the stack on 80/443 with a fake domain (the certificate step is
  skipped there), tests redirect, login page, setup link renewal, port isolation, socket proxy write refusal,
  revert with data kept, and re-apply. Mutation check on the safety properties.
- App tests (`go test`): full login + 2FA flow, CSRF, lockout, rate limit, admin rotate reveal-once, tenant
  isolation (404), locale parity, open redirect, spoofed forwarded address, setup link renewal, system status levels.
