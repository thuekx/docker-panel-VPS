# APP-02 — Node.js / Next.js, Go and Python websites, deployed from Git

Status: APP-02a BUILT 2026-11-12 (v1.10.0, Node.js from Git or own files); 02b/02c [DESIGN]. Designed 2026-11-12, after APP-01 (Laravel live as v1.9.0). Owner order: every stack first (APP-01, APP-02),
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

## APP-02a as built (v1.10.0)

- App `node`; Node.js 22, 24 (default), 26 = official `node:<version>-trixie` images (they carry git), pinned by digest.
  The same image builds and runs the app. Port fixed at 3000 (`PORT`), so the edge needs no per-site port.
- Source `git` (public https address, no user info, branch) or `files` (`wp/source/`, starter app with no
  dependencies); optional app folder (`dir`) for monorepos. Private repositories, env editor, database: APP-02b.
- Layout: `wp/releases/<n>/` (visible in the File Manager and in backups), `deploy/history.json` and
  `deploy/logs/<n>.log` (0600, outside the website folder). Last 3 good releases kept; history 30 entries.
- Build: `docker run --rm` as www-data, `--cap-drop ALL`, `no-new-privileges`, 2 CPUs, 1.5 GB, 1024 pids,
  15 minutes; repository and branch passed as environment values, never inside the script; `.git` removed from the
  release. Install: `npm ci` when there is a lock file, else `npm install`; then the build command.
- Switch: compose points `app` at the new release and `up --wait` (180 s, healthcheck = an HTTP answer below 500 on
  port 3000). On failure the previous release is put back and the deploy is marked failed. No downtime during the
  build; a few seconds while the container is recreated (blue-green is a later improvement).
- Harness: from files (starter, redeploy, failed build keeps the live release, rollback), and from Git with this
  repository's `packages/site-01/tests/node-app` on the branch under test (the repo check sets the branch).

## APP-02b as built (v1.11.0, first part)

- Apps `go` and `python` share the Node.js path (`IsRuntime`): same releases, history, build container, switch and
  rollback. Catalogue `Runtime{Version, Image, SecurityUntil, Default}` per app: `golang:<v>-trixie` (1.26, 1.27
  default), `python:<v>-trixie` (3.13, 3.14 default, 3.15), pinned by digest; the same image builds and runs.
- Install / build / start defaults per app (`spec.InstallCmd`, `DefaultBuild`, `DefaultStart`). Go: `go mod download`,
  `CGO_ENABLED=0 go build -trimpath -o server .`, `./server`; cache in `/tmp` of the build container. Python: venv
  `.venv` inside the release + `pip install -r requirements.txt`; build `true`; start `.venv/bin/python app.py`.
- Healthcheck for Go and Python: `curl` (present in both images) for an HTTP answer below 500 on port 3000.
- Environment values: `deploy/app.env` (0600, raw `KEY=VALUE`, read by compose with `format: raw`, so no quote or `$`
  handling), passed to the build too. Names only over the API and in the audit log; at most 100 keys, one line of
  at most 4096 characters each; reserved: PORT, HOST, HOSTNAME, HOME, PATH. Saving recreates the app container.
- Still to do in APP-02b: private repositories (deploy keys) and a database for apps.
