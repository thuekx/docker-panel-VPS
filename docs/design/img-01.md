# IMG-01 — Update image, per website

Status: BUILT 2026-11-13 (v1.16.0). Owner order 2026-11-12 19:34 WIB: a per-website "Update image" button; the panel
shows "update available" when the agent carries newer pinned images; one press makes a backup first, then recreates
the containers on the new images; images stay pinned by digest, never auto-"latest".

## How it works

- **Source of truth.** Every image is pinned by digest in `agent/internal/catalog`. A new agent release may carry newer
  pins (WordPress/PHP patch, MariaDB, PostgreSQL, Nginx, Node.js/Go/Python runtime, WP-CLI) or a new recipe of a
  built image (Laravel, PHP with PostgreSQL: the local tag holds a hash of the recipe, so a new base image means a new tag).
- **What runs now.** `GET /v1/sites/{id}/images` compares, per service, the image the container was created with
  (`docker compose ps`; for a service without a container, such as `cli`, the compose file) with the image the agent
  renders now. A container on an image that lost its name (`sha256:…`) counts as older.
- **No silent moves.** When the agent rewrites a compose file (WordPress Tools sync, a deploy), it keeps the images of
  the file it replaces (`keepImages`). A runtime deploy is the one exception: the app is built on the newest pinned
  runtime, so the `app` service takes it; the database keeps its image.
- **Update image** (`POST /v1/sites/{id}/images/update`, job kind `image`), one change at a time like every job:
  1. pull each new image that is not on the server yet (built images are never pulled); a failed pull changes nothing;
  2. a backup of kind `before-update` (same archive as BAK-01, the last 3 kept like `before-restore`);
  3. write the new compose file and, for built images, the new recipe, and build;
  4. `up -d --wait` (300 s); if the website is not healthy, the previous compose file and recipe are written back
     and started again; if that fails too, the job names the backup to restore.
- **Database majors.** The catalogue only moves a database within its major line (MariaDB 11.8, PostgreSQL 18), and
  MariaDB runs with `MARIADB_AUTO_UPGRADE=1`. A new major line would be a new choice, never an update.

## Panel

Overview card **Images** (`id="images"`): container, image it runs (`name:tag · digest start`), new image or "already
the newest", pill Up to date / Update available / Updating, and the **Update image** button with a confirm. The job
page shows pull, backup database, backup files, (build image), recreate containers; on failure, "previous images
back". The backup appears on the Backup page as "before update".

## Tests

- Agent unit tests: status (running image, unnamed image, up to date, unknown site), `keepImages`, deploy keeps the
  database image, update order (pull → backup → up), failures (pull, unhealthy start put back, both fail), built image.
- Panel tests: card in both languages, button only when an update exists, job page links.
- Harness (real Docker): the database of a WordPress site is moved to a local older tag, the agent reports it, Update
  image recreates it on the pinned digest with a `before-update` backup, the site answers, a second update is refused.
- Mutants: no `keepImages` on sync or deploy, no backup, no put-back, a failed pull ignored.
