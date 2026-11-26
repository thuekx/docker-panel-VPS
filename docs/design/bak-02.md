# Server backup and move to a new server (BAK-02)

Status: DESIGN, 2026-11-26. Roadmap slot 14b (RAYX-1 = A). Built on the recommended option of the scope question
(one archive with the panel and every website; the answer is recorded as BAK-2 when it lands).

## Goal
Move a whole Nostek Panel server to a new VPS with two steps: make one **server backup** in the panel and download
it (or copy it off the server), then run the installer on the new VPS with `--restore FILE`. Admins, two-step
login, settings, alerts, API keys and every website (files, database, settings, secrets, domains, security,
schedules) come back.

## The archive
- One file `nostek-server-YYYYMMDD-HHMMSS.npbk` in `/srv/nostek/server-backups/` (root only, 0700/0600), the
  newest 3 kept. Listed, downloaded and deleted on the panel's new **Server backup** page (Settings).
- **Always encrypted** with a passphrase typed when the backup is made (at least 12 characters, never stored, not in
  the audit log): PBKDF2-SHA256 (600 000 rounds, random 16-byte salt) gives an AES-256-GCM key; the stream is cut
  into 64 KiB chunks, each sealed with its counter and a last-chunk flag in the nonce, so a cut, reordered or
  changed file is refused. Go standard library only (agent rule).
- Inside (a tar): `manifest.json` (format `nostek-server-backup/1`, time, agent and panel version, host name, the
  websites with domain, app, engine and sizes, `complete`), `panel/panel.db` (a consistent copy made by the panel
  with SQLite `VACUUM INTO`), `panel/alerts.key`, and per website `sites/<id>/site.tar` (the website folder:
  `site.json`, compose and config files, `secrets/`, without `wp/`, the raw database folder, `backups/` and old
  static releases) plus `sites/<id>/backup.tar.gz` (a fresh BAK-01 backup: files and database dump).
- Made as an agent job (one at a time, the websites keep running); the panel shows progress like other jobs.
  Disk check first: refused when the free space is less than the websites' size plus 10 %.

## Restore on a new server
- `sudo bash nostek-install.sh INSTALL --domain panel.example.com --restore /root/nostek-server-….npbk`
  (passphrase asked on the terminal, or `--passphrase-file`). The installer checks the archive (passphrase,
  format, complete) **before** it changes anything, then installs as usual, then the agent restores:
  1. stop the panel, put `panel.db` and `alerts.key` in place (owner 65532), start the panel;
  2. for each website: unpack `site.tar` into `/srv/nostek/sites/<id>/`, start its stack (like a restore from the
     trash), then restore `backup.tar.gz` with the BAK-01 restore (database and files);
  3. a website that cannot come back (for example a native one when the native engine is not installed) is
     reported and skipped; the others continue.
- No admin setup link is printed: the admins of the old server sign in as before (two-step login included).
- DNS still points to the old server until it is changed; the websites answer on the new one as soon as it does
  (certificates are issued then).

## Not in this step
Scheduled server backups and a copy to S3 from the panel (the nightly restic job of HOST-02 already copies
`/srv/nostek` off the server where it is set up); restoring onto a server that already has websites.

## Tests (plan)
Agent: encryption round trip, wrong passphrase, cut/changed/reordered chunk refused, archive contents and modes,
disk check, restore of a Docker website end to end (harness with real Docker), skipped native website. Panel: page,
passphrase rules, passphrase never in the audit log or a page, download. Installer harness: `--restore` checks the
archive before any change, wrong passphrase stops early.
