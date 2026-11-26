# Server backup and move to a new server (BAK-02)

Status: BUILT v1.34.0, 2026-11-26. Roadmap slot 14b (RAYX-1 = A). Decision BAK-2 = A (one archive with the panel
and every website). User guide: [docs/guides/server-backup.md](../guides/server-backup.md).

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
  (passphrase asked on the terminal, or `--passphrase-file`). Needs release v1.34.0 or newer and a server without
  websites.
- Checks, in order: before any change, the file starts as a server backup and the passphrase has 12 characters or
  more; after the server base (updates, Docker, firewall) and **before the panel is installed**, the agent of the
  release (built in Docker, the image SITE-01 uses next) reads the whole file in a container without network:
  passphrase, every chunk, every entry the manifest names. A wrong passphrase stops there and the same command can
  run again. (A full check before the base would need the agent, which needs Docker; the base is harmless to
  repeat.)
- Then the panel and the agent are installed as usual and `nostek-agent server-restore` puts things back:
  1. `sites`: for each website, `site.tar` becomes its folder (with empty `wp/` and `db/`), password files and SFTP
     accounts come back, its images are built or pulled, the database container starts, and the BAK-01 restore
     loads the database and switches in the files; the stack then starts. A name that exists already is skipped; a
     failed website does not stop the others and its backup stays in `/srv/nostek/server-restore/<id>/`;
  2. `panel`: the installer stops the panel, `panel.db` and `alerts.key` are put in place (uid 65532, 0600, old
     SQLite side files removed), and the panel starts again; the agent restarts.
- **Native websites are skipped** in this step and their BAK-01 backup is kept in `/srv/nostek/server-restore/<id>/`:
  they need their Linux user, PHP-FPM pool and host database made first. The guide gives the manual steps (create
  the website again with the same domain, put the backup in its backups folder, restore).
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
