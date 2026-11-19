# Changelog

All notable changes to Nostek Panel. Versions follow [Semantic Versioning](https://semver.org/):
MAJOR for changes that need manual steps, MINOR for new features, PATCH for fixes. Dates in WIB.

## [1.23.0] — 2026-11-19

### Added
- **Security tab** for every website (SEC-01 wave 1), like cPanel's IP Blocker, Directory Privacy and Hotlink
  Protection. The rules run at the edge, so they work the same for every app and for Docker and native websites:
  - **IP Blocker:** block an address or a network (IPv4 or IPv6, up to /8 and /16 wide); its visits get 403 on
    every name of the website. The page shows your own address, and the server's own address cannot be blocked;
  - **password-protected folders:** a folder of the main address (or `/` for the whole website) asks for a user
    name and password. Several users per folder; a user can get a new password or be removed. The panel stores
    only a bcrypt hash: the password never reaches the agent, a log or the website;
  - **hotlink protection:** other websites cannot embed this website's images and media (403), with a list of
    sites that may. Visits without a referrer keep working.
- Every change runs as a job with its step on the page; a failed change puts the previous rules back.

## [1.22.0] — 2026-11-17

### Added
- **Domains tab** for every website (DOM-01), like cPanel's Domains, Aliases, Redirects and Track DNS:
  - every name of the website with where DNS points it now (A and AAAA), a verdict that says what to do (points
    here, points elsewhere, partly here, no record), and the certificate of every served name (valid until, being
    issued, or not valid);
  - a warning when an AAAA record points elsewhere: this server has no IPv6 yet, and the certificate can fail;
  - **add or remove extra names** (up to 10) for the same website; a name is served as soon as it points here;
  - **Check DNS again**: every name that now points here is served. When the domain itself is served for the first
    time, it becomes the main address and WordPress's addresses move from the preview name to the domain (links in
    posts included), with the previous state put back if that fails. This is the last step of a MIG-01 migration;
  - **redirects** per path (up to 50), to another path of the website or to another address, 301 permanent or 302
    temporary; the visit's query follows unless the target has its own.
- Every change runs as a job with its steps on the page. Docker websites recreate their web container (a few
  seconds); native websites only reload Nginx.

### Changed
- The Migrate tab's "Domains" button opens the new Domains tab.
- Every **Copy** button now confirms the copy: the button turns into "✓ Copied" for two seconds with a short
  animation, and an inline notice next to it says so (also read out by screen readers). When the browser refuses,
  the notice says so and the text is selected, ready for Ctrl+C.

## [1.21.0] — 2026-11-17

### Added
- **Move a WordPress site into the panel** (MIG-01). A WordPress website gets a **Migrate** tab with two ways. Both
  run on the website's preview address first, while the old site keeps running and is only read.
  - **Way 1: the Nostek Migrate plugin.** Download `nostek-migrate.zip` from the tab, install and activate it on the
    old site, copy the code from Tools → Nostek Migrate and paste it in the panel. The server then pulls the files
    and the database over HTTPS. The plugin is one PHP file (MIT, PHP 7.4+, WordPress 5.6+):
    - a 24-hour key, stored only as a hash, locked after 10 wrong tries in an hour;
    - read-only routes in small pieces that fit a 30-second shared-hosting limit (files in tar batches of 64 MB,
      large files in 32 MB ranges, tables in keyset pages);
    - the key is deleted when the move is done.
  - **Way 2: a cPanel backup**, for hosting without SSH. Upload the backup from the browser, or give an https
    download link that the server fetches itself. Accepted:
    - a cPanel full backup (the files in a nested `homedir.tar`, the databases in `mysql/`);
    - a home directory backup with the database backup (`.sql.gz`) as a second file;
    - a zip or tar.gz of the WordPress folder with a `.sql` dump.
    When the backup holds several WordPress sites (addon domains), the job stops before any change and the tab
    asks which one.
  - **What the move does:**
    - a `before-migrate` backup of the website first (one restore brings it back);
    - the old files come in owned by the website, caches and other backup plugins' archives stay behind;
    - the panel's own `wp-config.php` is kept, and the old one is saved as `wp-config.old.php`, which stops at once
      if opened over the web;
    - the database is loaded without `CREATE DATABASE`, `USE` or `DEFINER` lines, and the table prefix is carried;
    - the addresses are rewritten with WP-CLI (serialized data included) to the new https address;
    - caching drop-ins are moved aside;
    - at the end it checks that the site loads on the new address and has an administrator.
- The job card shows how far the current step is (for example `120/340 MB`).

### Security
- The server reaches the old site and the download link over HTTPS only, and only public internet addresses. Every
  connection is checked after DNS, so a redirect or a name that changes its answer cannot reach loopback, private
  networks or the server itself.
- The audit log names the way and the old host only, never the migration code or the link's query.

### Fixed
- Restoring a backup now puts back the WordPress table prefix it was made with. Restoring a `before-migrate` backup
  returns to the fresh website.

### Not in this release
- Multisite, databases other than MariaDB/MySQL, e-mail, and switching DNS.

## [1.20.0] — 2026-11-16

### Added
- **Native engine, optional** (NATIVE-01, decision NATIVE-1). Next to Docker, a WordPress, PHP or static website
  can run on the server's own Nginx, its own PHP-FPM (PHP 8.1-8.5 from the ondrej/php PPA) and the shared MariaDB.
  A quiet website then takes a few MB instead of 150-250 MB. Docker stays the default and every existing website keeps
  running as it is.
  - New host package **NATIVE-01** (`packages/native-01/native-01-host.sh`, PREFLIGHT/APPLY/VERIFY/REVERT). It installs
    Nginx (listening only on the edge network's address, port 8088, reachable from the edge only), PHP-FPM 8.1-8.5
    (the distribution pools stay off), MariaDB on its socket only and WP-CLI 2.12.0 (checksum pinned). Without it,
    nothing changes: the panel offers Docker only.
  - **Add website** gains an **Engine** choice (Docker recommended / Native) with what each means; the Overview shows
    the engine.
  - Each native website gets its own Linux user, its own PHP-FPM service and its own database user. Its PHP and every
    WP-CLI run of it work in a systemd sandbox:
    - a read-only system, where only its own folder is writable and other websites are not visible;
    - no access to the agent or Docker;
    - no loopback or private network addresses (the internet and the DNS resolver stay reachable);
    - open_basedir, process functions disabled, and memory, CPU and task limits.
  - Native websites have the same panel features as Docker ones: logs, start/stop/restart, the Resources card (PHP
    settings and the PHP memory limit), File Manager (files written as the website's user), Disk (the database size
    from MariaDB), backups and restores, the Database page (as the website's own database user), WordPress Tools,
    the system cron, trash and restore, and permanent delete (which also removes its database and Linux user).
  - Traefik reads the native websites' routes from a file provider (`/srv/nostek/edge/dynamic`, read-only), next to
    the Docker routes. It keeps ports 80/443 and the certificates.

### Not for native websites (yet)
- Laravel, Node.js, Go, Python and ready apps, Apache, and PostgreSQL stay Docker-only. "Update image" does not apply:
  apt keeps the host's Nginx, PHP and MariaDB patched. Moving a website between Docker and native is a backup and a
  restore into a new website (MIG-01).

## [1.19.0] — 2026-11-16

### Added
- **Disk usage and disk analyzer** (DISK-01, like cPanel's Disk Usage, 2026-11-16).
  - Every website gets a **Disk** tab: what its files, database, backups and deleted files take, a drill-down of its
    folders from largest to smallest (click a folder to go in), and its 20 largest files with a link to their folder
    in the File Manager. Symbolic links are never followed; a very large folder stops counting after 20 seconds and
    says so.
  - A new **Disk** page (menu, and "Analyze disk" on the Dashboard's Disk card): the server's disk, every website by
    size, the website trash, and what Docker keeps (images, containers, volumes, build cache) with what can be freed.
  - Two safe clean-ups: **Remove unused images** removes only images no website names and no container uses (the
    images of your websites, WP-CLI, built images and the panel's own images stay; older versions kept after Update image go), and **Clear
    build cache** removes build cache older than a day. Neither runs while a website is being changed.

## [1.18.0] — 2026-11-16

### Added
- **Resources per website** (RES-01). The Overview gains a **Resources** card. Every website keeps
  running on our standard limits until they are raised, and they can only be raised (or set back to the
  standard), never set below it.
  - **Memory per container**: the standard of the template (for example PHP 512 MB, Nginx 128 MB, database 512 MB),
    or 768 MB, 1 GB, 1.5 GB, 2 GB. Memory is taken from the server only while it is used; the limit stops one website
    from taking all of it.
  - **PHP settings**, like cPanel's MultiPHP INI Editor: `memory_limit` (256 MB to 1 GB), the largest upload
    (`upload_max_filesize` and `post_max_size`, 128 MB to 1 GB; the website's Nginx accepts the same size),
    `max_execution_time` (120 to 900 s) and `max_input_vars` (1000 to 10000). A higher `memory_limit` raises the
    memory of the containers that run PHP with it, so PHP always has room.
  - The card shows the limits of all websites together against the server's memory, and warns when they add up to
    more than the server has.
  - Saving recreates the website's containers with the new limits (a few seconds offline); if they do not come up,
    the previous limits are put back and the website starts on them again. Images stay as they are (IMG-01).

## [1.17.0] — 2026-11-14

Live on the production server since 2026-11-16 01:38 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Ready apps** (APP-03, first wave). "Add website" gains a second group, **Ready apps**: **Uptime Kuma** (website
  and service monitoring) and **n8n** (workflow automation). Each runs its official image as it is, pinned by digest,
  with nothing to choose but the domain.
  - The app runs as www-data with all its data (its SQLite database too) in the website folder, so the File Manager,
    backups, restores and **Update image** work as for every website. A backup stops the app for a few seconds so the
    copy of its database is consistent.
  - It is reachable only through the panel's HTTPS edge; it may reach the internet (monitor checks, workflow calls).
  - After creating it, open it at once and make its first account: the first visitor sets it.
  - Logs of the app are on the Overview.
- **Start, Restart, Stop and Delete show that they are working**: the pressed button spins and says
  what runs ("Stopping…"), the other buttons wait, and a note says it can take up to 3 minutes; the page shows the
  result by itself when it is done. Also on the restart button of each container.

### Fixed
- The Overview's log button of a Node.js, Go or Python website asked the agent for a service it refused; logs of the
  app container now open.
- The progress page of a new website listed the WordPress steps for every app: a Go, Node.js or Python website
  showed "Installing WordPress…" and its build step never turned. The list now follows the app from the start
  (plain PHP, static and ready apps have no install step), and the page reloads itself if the agent reports a step
  the list does not have.

## [1.16.0] — 2026-11-13

Live on the production server since 2026-11-14 22:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Update image** for every website (IMG-01). A new card **Images** on the Overview lists each container, the image
  it runs (name, tag and the start of its digest) and, when this panel version carries a newer pinned image, the new
  one, marked **Update available**.
  - One press of **Update image** downloads the new images first (if a download fails, nothing changes), makes a
    backup ("before update" on the Backup page), then restarts the containers on the new images. If the website does
    not come up healthy, the previous images are put back; if even that fails, the job names the backup to restore.
  - Built images (Laravel, PHP with PostgreSQL) are built again from the new recipe.
  - Images stay pinned by digest; nothing ever runs a floating "latest".

### Changed
- A website no longer moves to newer images by itself. When the agent rewrites a compose file (WordPress Tools, a
  deploy), the containers keep the images they run; only **Update image** changes them. A deploy of a Node.js, Go or
  Python website still builds and runs the app on the newest pinned runtime, and its database keeps its image.

## [1.15.0] — 2026-11-13

Live on the production server since 2026-11-14 22:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Database choice per package** (DB-FLEX, as on cPanel). "Add website" now offers the databases each package
  supports:
  - **WordPress**: MariaDB (WordPress supports MySQL and MariaDB only).
  - **Laravel**: MariaDB or **PostgreSQL 18**. With PostgreSQL the Laravel image also gets the `pdo_pgsql`
    extension, and `.env` uses `DB_CONNECTION=pgsql`.
  - **PHP**: no database, MariaDB or **PostgreSQL 18**. With PostgreSQL the website's PHP image (Apache or PHP-FPM) is
    built on the server from its official image plus the `pgsql` and `pdo_pgsql` extensions; the code finds
    `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_ENGINE` and the password file `DB_PASSWORD_FILE`.
  - **Node.js, Go, Python**: no database, MariaDB or PostgreSQL 18, as in 1.13.0.
  - **Static**: no database.
  - The Database page, export and import, backups and restores work for every package on either engine. Existing
    websites keep their database and images unchanged.

## [1.14.0] — 2026-11-13

Live on the production server since 2026-11-14 22:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Auto-deploy on push** for Node.js, Go and Python websites built from Git (APP-02c). A new card **Auto-deploy** on
  the Overview turns it on and shows, once, the webhook address (`https://<panel>/hooks/<website>`) and a secret to
  paste into the repository's webhook settings (GitHub, Gitea or GitLab; the steps are on the card).
  - Every push to the website's branch then builds and starts a new release, with the same checks as Deploy: a
    failing build leaves the live release running. Pushes to other branches, deleted branches and other events are
    ignored. A push that arrives while another job runs waits for it and starts after it.
  - Each delivery must be signed with the website's secret (HMAC-SHA256, or GitLab's token); the secret stays on the
    server, readable by root only, and is never shown again (make a new one if it is lost; the old one stops at once).
  - The card lists the last deliveries and what happened; the deploy history marks releases started by a push.

## [1.13.0] — 2026-11-13

Live on the production server since 21:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **A database for Node.js, Go and Python websites** (APP-02b). "Add website" asks for one: no database, **MariaDB**
  or **PostgreSQL 18**, each on its official image pinned by digest, on the website's own private network.
  - The app gets the connection values in its environment, ready to use: `DATABASE_URL` (`mysql://…` or
    `postgres://…`) and `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`. The password is made on the server
    and is never shown; the Overview lists only the names.
  - The Database page (tables, SQL console, export and import) and website backups and restores work for both
    engines, with the engine's own tools (`pg_dump` and `psql` for PostgreSQL).
  - The Overview names the engine (MariaDB or PostgreSQL).

### Fixed
- Restoring the files of a Node.js, Go or Python website from a backup failed: it tried to stop a web server
  container these websites do not have. It now stops the app container.

## [1.12.0] — 2026-11-13

Live on the production server since 17:05 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Private Git repositories** for Node.js, Go and Python websites (APP-02b). Give the SSH address of the repository
  (for example `git@github.com:user/app.git`); the panel makes a **deploy key** for that website only and shows it
  on the approve card, with the steps to add it on GitHub, GitLab or Bitbucket (read only). The first build then
  reads the repository with it.
  - The private half of the key never leaves the server: it is kept in a file only the website's build can read and
    is mounted read only into the build container. The Overview shows the public key and its fingerprint.
  - The repository's host key is pinned on the first fetch; if it ever changes, builds stop (the live release keeps
    running) instead of fetching from an unknown server.

## [1.11.0] — 2026-11-12

Live on the production server since 17:05 WIB 2026-11-13, together with 1.12.0 (`site-01` APPLY + VERIFY PASS,
`panel-01 UPDATE` + VERIFY PASS).

### Added
- **Go and Python websites** (APP-02, second part). "Add website" now offers Go (1.26, 1.27 default) and Python
  (3.13, 3.14 default, 3.15) next to Node.js, on their official images pinned by digest. They use the same Deploy,
  history, build log and rollback as Node.js apps.
  - Go: `go mod download`, then `go build` to `./server` (or your build command), started with `./server`.
  - Python: its own virtual environment `.venv` with the packages of `requirements.txt`, an optional build command
    (for example `collectstatic`), and your start command (for example gunicorn or uvicorn on `$PORT`).
  - A website built from its own files starts with a small starter app of its runtime.
- **Environment values** for Node.js, Go and Python apps: a card on the Overview to add, change and delete values
  (for example `DATABASE_URL` or API keys). The panel only ever shows the names, never the values; they are kept in a
  file only root can read, outside the website folder, and the app restarts with them on save. `PORT`, `HOST`,
  `HOSTNAME`, `HOME` and `PATH` stay set by the panel.
- **Delete permanently** in the website trash: type the domain and the website's files and database are removed at
  once, so the domain can be used again right away (before, it stayed reserved for 7 days). Adding a website whose
  domain is still held by the trash now says so.

### Fixed
- The Websites page showed "restore failed" in green; failures now show as a warning.

## [1.10.0] — 2026-11-12

Live on the production server since 22:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS), together
with 1.9.1.

### Added
- **Node.js websites** (APP-02, first part). "Add website" offers Node.js: Express, NestJS, Nuxt, SvelteKit, Next.js
  and other Node.js apps, on the official Node.js image (22, 24 default, or 26), pinned by digest.
  - Source: a public **Git repository** and branch, or the website's own **files** (upload them with the File
    Manager; a small starter app is put there first). An **app folder** can be named for a monorepo (`apps/web`).
  - The panel runs `npm ci` (or `npm install`), your build command and your start command; the app listens on the
    port in `PORT` (3000). Build and start commands have defaults and can be changed.
  - **Deploy** on the Overview: every deploy builds a new release next to the live one in a separate, limited
    container (2 CPUs, 1.5 GB, 15 minutes, no privileges). The website switches only when the new release answers;
    a failed build or a release that does not start leaves the previous one live.
  - Deploy history with commit, result and the **build log** of every release, and **rollback** to one of the last
    3 good releases.
- Next: Go and Python apps, private repositories (deploy keys), environment values in the panel, a database for
  Node.js apps, and an auto-deploy webhook (APP-02 parts b and c).

## [1.9.1] — 2026-11-12

Released to the production server with 1.10.0 (22:29 WIB).

### Added
- **Choose the Laravel version** when adding a Laravel website: Laravel 13 (newest, default) or Laravel 12. The panel
  says which PHP version each one needs (13: PHP 8.3 or newer, 12: PHP 8.2 or newer) and refuses a combination that
  cannot work. The Overview shows the version.

### Fixed
- Dashboard on wide screens: the six status cards now always fill the row (6 across on wide screens, 3, 2 or 1 on
  smaller ones) instead of leaving an empty gap on the right. The Websites card names every app, not only WordPress.

## [1.9.0] — 2026-11-12

Live on the production server since 20:34 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **Laravel websites** (APP-01, second wave). "Add website" offers Laravel: a fresh Laravel project, ready to use,
  with its own MariaDB database, a queue worker and the scheduler, on Nginx + PHP-FPM (PHP 8.1–8.5).
  - The PHP image for Laravel is built on the server from official images only (official PHP-FPM + `pdo_mysql`,
    official Composer), pinned by digest. The first Laravel website per PHP version takes 1–3 minutes longer.
  - The panel writes `.env` (production mode, the website address, the database). The database password is never
    in `.env` or an environment value: the panel hands it to Laravel from a secret file.
  - Overview: a Laravel card with safe artisan commands (migrate, optimize, clear caches, restart queue), and logs
    and restart for the queue worker and the scheduler.

## [1.8.0] — 2026-11-12

Live on the production server since 19:32 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS).

### Added
- **More than WordPress** (APP-01, first wave). "Add website" now asks which app:
  - **WordPress** as before.
  - **PHP**: your own PHP code (plain PHP, CodeIgniter, Slim …) on Apache or Nginx with PHP 8.1–8.5, and a MariaDB
    database if you tick it. The app reads `DB_HOST`, `DB_NAME`, `DB_USER` and the password from the file named in
    `DB_PASSWORD_FILE`. Nginx sends every path that is not a file to `index.php`.
  - **Static site**: HTML, or the built `dist/` of React, Vue, Svelte or Astro. Nginx only, the folder is read-only,
    unknown paths fall back to `index.html` (single-page apps).
  - A small starter page is put in place; replace it in the File Manager.
- Overview, File Manager, Stack, Logs, Backup and PHP settings work for every app. The WordPress tab shows only for
  WordPress, the Database tab only for websites with a database.

### Changed
- The database tools log in with the website's own database user instead of assuming WordPress.

Laravel follows in the next wave (it needs its own PHP image with `pdo_mysql` and Composer).

## [1.7.0] — 2026-11-12

Live on the production server since 18:31 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS,
certificate valid). The same update brought 1.6.1.

### Added
- **Backup** tab per website (BAK-01):
  - **Make a backup now**: one archive with every file and a consistent copy of the database.
  - **Restore** everything, files only or database only. A safety copy of the current state is always made
    first, so a restore can be undone; the domain must be typed to confirm.
  - **Download** and **delete** backups.
  - **Automatic daily backup** (after 02:00 WIB) with 1–30 days kept. Backups made by hand are never pruned.
  - Live progress while a backup or restore runs; plain-language messages when something fails.
- Backups live in the website's own folder under `/srv`, so the nightly off-site backup (03:30 WIB) carries them too.
- nostek-agent: `/v1/sites/{id}/backups…` routes; restore refuses archives of another website and any path that
  leaves the website folder.

## [1.6.1] — 2026-11-12

Released with 1.7.0 in one update (18:31 WIB).

### Fixed
- WordPress Tools: the plugin list could fail to load ("Some details are missing: plugins: WP-CLI failed …")
  when WP-CLI reported a must-use plugin with `"update": false` instead of text. Such values are now read as text,
  so one odd plugin no longer hides the whole list.

### Changed
- When part of the WordPress overview cannot be read, the page now explains it at the bottom in plain words
  (what is missing, that the website keeps running, what to do); the raw WP-CLI output is folded under
  "Technical details".

## [1.6.0] — 2026-11-12

Live on the production server since 16:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

### Added
- **WordPress Tools** per website (WP-01, like WP Toolkit), a new WordPress tab:
  - Site card with the address, admin account and **one-click login to wp-admin**. The link works once, for
    60 seconds; WordPress keeps only its SHA-256 and the link is never written to a log.
  - **Dashboard**: WordPress version and updates for core, plugins and themes ("Update everything" in one go);
    HTTPS certificate status with expiry date; security measures in place; integrity check of WordPress core
    and wordpress.org plugins against their official checksums.
  - Switches: **debugging** (PHP errors go to the website's Logs, never to visitors or a public debug.log),
    **search engine indexing**, **system cron** (the server runs wp-cron.php every 5 minutes instead of on
    visits) and **maintenance mode** (visitors get a 503 page, logged-in admins still see the site).
  - **Plugins** and **Themes** tabs: activate, deactivate, update, delete, auto-update on/off, install from
    wordpress.org by slug.
  - **WP-CLI** console: one command at a time in the website's own WP-CLI container, as www-data, without a
    shell; interactive commands and `--ssh`, `--http`, `--path` are refused. Only the command name goes to the
    audit log, never its arguments.
- nostek-agent: `/v1/sites/{id}/wp…` routes; a small must-use plugin `nostek-panel.php` in each website for the
  login link and maintenance mode.

### Changed
- The WP-CLI container of each website can reach the internet (for updates and checksums). The agent rewrites
  a website's compose file the first time WordPress Tools open it; running containers are not restarted.

## [1.5.0] — 2026-11-12

Live on the production server since 15:37 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

### Added
- **File Manager** per website (SITE-02): browse folders, open and edit text files (up to 2 MB, line ends kept),
  upload several files at once (up to 256 MB each, with a progress bar), download a file or a whole folder as .zip,
  extract a .zip in place, new folder and new file, rename or move, permissions (rwx only, never setuid). Deleting
  moves to a per-website trash that keeps files 7 days and can restore them.
- Everything works only inside the website's WordPress folder: no path, link or `..` reaches the database files,
  the website's secrets or the host. Files written by the panel belong to www-data like the files WordPress writes.
- **Database** per website: name, user, version, size and tables with rows and size; SQL console (SELECT shows up
  to 1000 rows; errors are shown as MariaDB reports them); export as .sql.gz; import .sql or .sql.gz up to 512 MB.
  Every import first takes a copy of the current database and keeps the newest 3, so an import can be undone.
- Tabs on each website: Overview, File Manager, Database.
- nostek-agent: `/v1/sites/{id}/files…` and `/v1/sites/{id}/db…` routes. The database password is read inside the
  database container; it never appears in an argument list, a log or an answer. The audit log records every file
  and database change, never file content or SQL text.

### Changed
- The edge proxy allows 30 minutes to read a request (was 60 seconds), so large uploads finish.
- Creating a website pulls only images the server does not have yet. Every image is pinned by digest, so a copy
  already on the server is the exact same image; this avoids Docker Hub rate limits (HTTP 429) on busy days.

## [1.4.0] — 2026-11-12

Live on the production server since 14:22 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

### Added
- **Stack** card on each website (STACK-01): every container of the site (web server, PHP-FPM, database) with
  image, state and health, CPU and memory with bars, uptime and restart count. Values update by themselves every
  few seconds; a state change swaps the card in. Logs and restart per container.
- Website list shows how many containers run (for example `3/3 running`).
- nostek-agent: `GET /v1/sites/{id}/stack` and `POST /v1/sites/{id}/services/{web|php|db}/restart`.

## [1.3.0] — 2026-11-12

Live on the production server since 01:07 WIB (`panel-01 UPDATE` + VERIFY PASS, certificate valid).

### Added
- **Profile** page: change your password (current password needed; every other device is signed out),
  theme (Light, Dark, Follow system), highlight colour (Teal default, Ocean, Violet, Sunset, Rose) and recovery codes.
- Creating a website shows live progress without reloading the page: progress bar, each step with its own
  animation, elapsed time, and the finished result slides in when done.

### Changed
- More colour: dashboard cards, website list and headings get their own colour tones and icons; teal stays the
  default brand colour.
- Theme and recovery codes moved from Settings to Profile.

## [1.2.0] — 2026-11-11

Live on the production server since 16:41 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` PASS).

### Added
- **Websites** (SITE-01): add a WordPress website with Apache or Nginx + PHP-FPM, PHP 8.1 to 8.5 (8.1 marked end of
  life), extra domains that redirect to the main address, `www` or bare domain as main address. An approve card shows
  what is created before anything changes; progress is shown live; the first admin password is shown once.
- Website list with honest status (never green when unknown), start, stop, restart, logs, delete to a 7-day trash
  and restore.
- Preview addresses (`<site>.<preview zone>`) so a website can be built before its domain moves to this server.
- **nostek-agent**: the host service that is the only part allowed to change Docker; fixed actions, strict input
  checks, audit log. Package `site-01` installs, updates, verifies and removes it.

### Changed
- The panel runs on its own network with the edge proxy; websites cannot reach it.

## [1.1.0] — 2026-11-11

Live on the production server since 15:48 WIB (updated with `UPDATE`, VERIFY PASS).

### Added
- Appearance setting: Light, Dark or Follow system. Switch in the header and in Settings; saved on the account
  (before login: per browser).
- Release version shown in the panel (`v1.1.0`) instead of a commit id; `VERSION` file and this changelog.
- Package `panel-01`: `UPDATE` mode builds the new version while the current one keeps running, then swaps;
  on any failure the previous version is started again.
- Package `panel-01`: the nightly backup result is recorded after every run, so the dashboard card
  "Last backup" shows the real result from the next nightly backup on.

### Changed
- Package `panel-01`: results show the release (`version=v1.1.0`); the APPLY summary no longer asks for a
  setup link when an admin already exists.

## [1.0.0] — 2026-11-11

First release, live on the production server since 15:06 WIB.

### Added
- Nostek Panel (Go): one-time setup link for the first admin, login with Argon2id passwords and mandatory
  TOTP 2FA, 10 recovery codes, break-glass admin password rotate via one-time reveal link.
- Dashboard: server, memory, disk, load, Docker and last backup; unknown or stale values are never green.
- Audit log (time in WIB), settings (time zone, recovery codes), Indonesian and English.
- UI in the Nostek teal brand with Motion and anime.js, honouring reduced motion.
- Core stack: edge HTTPS proxy (only service with open ports), read-only Docker socket proxy, panel
  (non-root, read-only filesystem).
- Package `panel-01` (PREFLIGHT, APPLY, SETUP-LINK, ADMIN-ROTATE, VERIFY, REVERT) with harness and mutants.

### Fixed
- APPLY no longer fails while the HTTPS certificate is still being issued (#4).
- The 2FA QR code is shown; a new 2FA key is made on each visit of the enrol page until 2FA is on (#5).
