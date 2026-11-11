# Changelog

All notable changes to Nostek Panel. Versions follow [Semantic Versioning](https://semver.org/):
MAJOR for changes that need manual steps, MINOR for new features, PATCH for fixes. Dates in WIB.

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
