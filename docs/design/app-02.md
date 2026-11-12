# APP-02 — Node.js / Next.js, Go and Python websites, deployed from Git

Status: [DESIGN] 2026-11-12, after APP-01 (Laravel live as v1.9.0). Owner order: every stack first (APP-01, APP-02),
then IMG-01 (update image button), then APP-03 (ready-app catalogue).

## What the owner gets

"Add website" gains **Node.js** (Express, NestJS, Nuxt, SvelteKit, Remix, Next.js), **Go** and **Python** (Django,
FastAPI, Flask). Unlike PHP, these apps must be **built and started**, so the website has a **Deploy** tab:

- **Source**: a Git repository URL + branch (public, or private with a read-only deploy key the panel generates and
  shows once as a public key to paste into GitHub/GitLab), or a .zip uploaded in the File Manager.
- **Deploy** button + history (who, when, commit, result, build log). The running version keeps serving until the
  new one is built and healthy; a failed build never takes the website down.
- **Settings**: install/build/start commands with sensible defaults per stack, the port the app listens on, and
  environment values (secret values stored in files, shown masked, never in compose.yml or argv).
- Optional **auto-deploy** webhook per branch (a secret URL to paste in GitHub) — wave 2 of APP-02.

## How it runs (same edge, same tabs as every website)

| Stack | Build container (throwaway) | Runtime | Default commands |
|---|---|---|---|
| Node.js | official `node:<lts>` | same image, non-root `node` | `npm ci` · `npm run build --if-present` · `npm start` |
| Next.js | official `node:<lts>` | same image | `npm ci` · `next build` · `npm start` (standalone when configured) |
| Go | official `golang:<v>` | official `debian:<v>-slim`, non-root (only the built binary is copied in) | `go build -o /app/server .` · `/app/server` |
| Python | official `python:<v>-slim` | same image, non-root | `pip install -r requirements.txt` · `gunicorn` / `uvicorn` from the form |

- Every image official and pinned by digest in `catalog`; versions offered: Node 20/22/24 LTS, Go current two,
  Python 3.12/3.13.
- **Build**: a separate short-lived container with CPU/RAM/time limits, only outbound internet (for npm/pip/go
  modules), no access to the panel, the agent socket or other websites; writes into a fresh release folder.
- **Release folders**: `releases/<n>/` per deploy, `current` points to the live one; keep the last 3 for one-click
  rollback. Files tab shows the source; Backup carries the source, `.env` files and the database.
- **Run**: `app` service (non-root, `no-new-privileges`, memory limit, read-only root where the stack allows),
  Traefik routes the domain to the app's port; optional MariaDB or PostgreSQL like PHP apps.
- **Health**: the app must answer on its port within the start timeout before traffic switches.

## Phasing

1. **APP-02a**: Node.js + Next.js from Git (public) or .zip; Deploy tab with history and rollback; env editor.
2. **APP-02b**: Go and Python; private repos with deploy keys; PostgreSQL option.
3. **APP-02c**: auto-deploy webhook per branch.
