# Changelog

All notable changes to Nostek Panel. Versions follow [Semantic Versioning](https://semver.org/):
MAJOR for changes that need manual steps, MINOR for new features, PATCH for fixes. Dates in WIB.

## [1.6.0] — 2026-11-12

Running at the owner's server since 16:29 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

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

Running at the owner's server since 15:37 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

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

Running at the owner's server since 14:22 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` + VERIFY PASS, certificate valid).

### Added
- **Stack** card on each website (STACK-01): every container of the site (web server, PHP-FPM, database) with
  image, state and health, CPU and memory with bars, uptime and restart count. Values update by themselves every
  few seconds; a state change swaps the card in. Logs and restart per container.
- Website list shows how many containers run (for example `3/3 running`).
- nostek-agent: `GET /v1/sites/{id}/stack` and `POST /v1/sites/{id}/services/{web|php|db}/restart`.

## [1.3.0] — 2026-11-12

Running at the owner's server since 01:07 WIB (`panel-01 UPDATE` + VERIFY PASS, certificate valid).

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

Running at the owner's server since 16:41 WIB (`site-01` APPLY + VERIFY PASS, `panel-01 UPDATE` PASS).

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

Running at the owner's server since 15:48 WIB (updated with `UPDATE`, VERIFY PASS).

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

First release, running at the owner's server since 15:06 WIB.

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
