# APP-01 — One-click apps besides WordPress

Status: wave 1 BUILT 2026-11-12 (v1.8.0: plain PHP + static). Laravel moved to wave 2 (it needs its own image with
`pdo_mysql` and Composer; the WordPress images have only `mysqli`). Owner asked: one-click for React, Next.js,
Laravel, Go, plain PHP and other popular stacks, ready to use.

## Opinion

Worth doing: most new projects on a VPS are not WordPress. It fits the current design without new risk:
**one app = one Docker stack** behind the same edge (automatic HTTPS, domain + preview address), with the same
Files, Database, Stack/Logs and (after BAK-01) Backup tabs. Only the "what runs inside" changes.

Two kinds of templates, because they start differently:

| Kind | Starts as | Example |
|---|---|---|
| **Starter** | a working skeleton generated at create time, the owner edits or replaces it | plain PHP, Laravel, static |
| **From code** | the owner's own code from a Git repository (public, or private with a read-only deploy key) or a .zip upload; the panel builds it in a throwaway build container and runs it | React/Vite, Next.js, Node, Go, Python |

## Template list (first wave → later)

| Template | Runtime (pinned by digest) | Database | Notes |
|---|---|---|---|
| Static site (HTML / built React, Vue, Svelte, Astro) | Nginx | none | upload or build `npm run build`, serve `dist/` |
| React / Vue SPA (Vite) | Node LTS build → Nginx | none | SPA fallback to index.html |
| Next.js | Node LTS (standalone output) | optional MariaDB/PostgreSQL | `next build`, `node server.js`, non-root |
| Node.js (Express, NestJS, Nuxt, SvelteKit, Remix) | Node LTS | optional | start command + port from the form |
| Laravel | PHP-FPM 8.2–8.5 + Nginx, Composer | MariaDB (default) or PostgreSQL | `.env` written by the panel, queue worker + scheduler containers, `artisan migrate` button |
| Plain PHP | PHP 8.1–8.5 + Apache or Nginx | MariaDB | same PHP catalogue as WordPress |
| Go | build with golang, run in distroless | optional | `go build ./...` from the repo, port from the form |
| Python (Django, FastAPI, Flask) | Python 3.12+ with gunicorn / uvicorn | optional PostgreSQL | later wave |
| Ready apps (later catalogue) | Ghost, n8n, Uptime Kuma, Strapi … | per app | APP catalogue after the first wave |

## Panel features per app

Create wizard (domain, template, version, database yes/no) · Deploy button and deploy history with logs ·
optional auto-deploy webhook per Git branch · environment variables editor (secret values stored in files,
shown masked) · restart, logs, stack view · Files and Database tabs as for WordPress.

## Security

Builds run in a separate short-lived container with CPU/RAM/time limits and no access to the panel or other sites;
the running app is non-root, `no-new-privileges`, with memory limits like WordPress sites; app containers sit
only on the edge network and their own back network; secrets never in git, argv, logs or the page.

## Proposed phasing

- **APP-01 wave 1 (v1.8.0, built)** plain PHP (Apache or Nginx, PHP 8.1–8.5, optional MariaDB) and static site
  (HTML or a built React/Vue/Svelte/Astro `dist/` uploaded through Files, SPA fallback to `index.html`).
- **APP-01 wave 2** Laravel (own pinned PHP-FPM image with `pdo_mysql` + Composer, `.env` by the panel, queue worker,
  scheduler, `artisan migrate` button) and React/Vite built from Git in a throwaway build container.
- **APP-02** Node.js / Next.js, Go, Python, Git deploy webhook.
- **APP-03** Ready-app catalogue (Ghost, n8n, Uptime Kuma, …).

## Wave 1 as built (v1.8.0)

- `site.json` gains `app` (`""` = WordPress, `php`, `static`) and `database` (php only). Old sites read as WordPress.
- **Plain PHP** reuses the pinned WordPress images per PHP version with `entrypoint: ["docker-php-entrypoint"]`
  and `apache2-foreground` / `php-fpm`, so WordPress is never copied in. The site folder `wp/` is the document root.
  With a database: MariaDB 11.8 as for WordPress, database and user `app`; the app reads `DB_HOST`, `DB_NAME`,
  `DB_USER` and `DB_PASSWORD_FILE=/run/secrets/db_password` (the password is never an environment value).
  Nginx serves files directly and sends the rest to `index.php` (front controller).
- **Static** runs only nginx 1.30-alpine with the folder mounted read-only, SPA fallback to `index.html`, hidden
  files refused, healthcheck on `/.nostek-health`.
- A starter page (`index.php` or `index.html`) is written at create time; the owner replaces it in Files.
- The panel shows the WordPress tab only for WordPress and the Database tab only for sites with a database. Backup,
  Files, Stack, Logs, PHP settings (PHP sites), domain and preview address work as for WordPress. Agent routes for
  WordPress tools and databases refuse other apps with a plain 400.
- Tests: unit tests in both modules, real-Docker harness section APP-01, 4 extra mutants.
