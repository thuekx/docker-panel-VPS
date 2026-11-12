# APP-01 — One-click apps besides WordPress

Status: wave 1 LIVE 2026-11-12 19:32 WIB (v1.8.0: plain PHP + static); wave 2 Laravel BUILT 2026-11-12 (v1.9.0). Laravel moved to wave 2 (it needs its own image with
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

## Wave 2 as built (v1.9.0): Laravel

- Owner decision 2026-11-12 19:34 WIB: the Laravel image is **built on the server from official images**, not a
  community image. Recipe (`catalog.LaravelDockerfile`): the pinned official WordPress PHP-FPM image of the chosen
  PHP version (the same one WordPress sites use; WordPress is never started) + `pdo_mysql` + `unzip`, and the
  `composer` binary from the pinned official `composer:2` image. Local tag `nostek-local/laravel:php<v>-<hash of
  recipe>`, so a new recipe never reuses an old build. Built once per PHP version (1–3 minutes the first time).
- Services: `web` (Nginx, root `public/`), `php` (PHP-FPM), `queue` (`queue:work`), `scheduler` (`schedule:work`),
  `db` (MariaDB 11.8, database and user `app`). php, queue and scheduler run as www-data (33).
- Create: files → build PHP image → pull images → install Laravel (`composer create-project --no-scripts`,
  `.env` from `.env.example`, `key:generate`, `package:discover`, `storage:link`, `migrate --force`) → start.
- `.env`: production, debug off, `APP_URL`, `DB_*` without a password. The database settings are also environment
  values (they win over `.env`). The password stays a file: a PHP prepend file (`auto_prepend_file`) reads
  `DB_PASSWORD_FILE` and hands the value to Laravel before it starts, for the web, artisan, queue and scheduler.
- Panel: Laravel card on the Overview with a fixed list of artisan commands (migrate, optimize, optimize:clear,
  queue:restart); logs and restart for queue and scheduler; Database, Files, Backup tabs as for every site.
- Not yet: Laravel from the owner's Git repository (APP-02 with the Git deploy), Horizon/Octane, Redis.

## Laravel version choice (v1.9.1)

- Owner asked 2026-11-12 ~20:40 WIB for a choice of Laravel version. "Add website" → Laravel now offers
  **Laravel 13** (newest, default, needs PHP 8.3 or newer) and **Laravel 12** (needs PHP 8.2 or newer)
  (`catalog.Laravel`). The agent refuses a version it does not know, a PHP version below the minimum, and a
  version on any app other than Laravel. The site remembers its version (`site.json`), shown on the Overview.
- Install uses `composer create-project laravel/laravel:^<version>.0`, so it takes the newest release of that
  major version. Sites made before v1.9.1 have no stored version; nothing changes for them.
