# Changelog

All notable changes to Nostek Panel. Versions follow [Semantic Versioning](https://semver.org/):
MAJOR for changes that need manual steps, MINOR for new features, PATCH for fixes. Dates in WIB.

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
