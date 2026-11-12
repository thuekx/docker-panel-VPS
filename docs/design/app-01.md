# APP-01 — One-click apps besides WordPress

Status: [PROPOSED] 2026-11-12, roadmap slot after BAK-01. Owner asked: one-click for React, Next.js, Laravel, Go,
plain PHP and other popular stacks, ready to use.

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

- **APP-01** Static/React (Vite), plain PHP, Laravel (biggest demand, reuses the PHP + Nginx parts we have).
- **APP-02** Node.js / Next.js, Go, Python, Git deploy webhook.
- **APP-03** Ready-app catalogue (Ghost, n8n, Uptime Kuma, …).
