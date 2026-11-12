# BAK-01 — Website backup and restore from the panel (v1.7.0)

Roadmap row 10. Every website gets a **Backup** tab: make a backup now, download it, restore it (everything,
files only or database only), delete it, and switch on a daily backup with a chosen number of days kept.

## What a backup is

One `.tar.gz` per backup, in the website's own folder `/srv/nostek/sites/<id>/backups/` (root only, files 0600):

| Entry | What |
|---|---|
| `manifest.json` (always first) | format `nostek-site-backup/1`, website id, domain, web server, PHP, kind, time, file count, sizes, `complete: true` |
| `site.json` | the website's settings at backup time (no secret: passwords live in `secrets/`, which is not archived) |
| `database.sql` | `mariadb-dump --single-transaction --quick --routines --events --add-drop-table` (consistent while the site runs) |
| `wp/...` | every file of the website with owner, permissions and times; links are stored as links, never followed |

Names: `manual-YYYYMMDD-HHMMSS.tar.gz` (from the panel), `daily-…` (schedule), `before-restore-…` (safety copy).
The archive is written as `.part` and renamed only when complete, so a half-written backup is never listed.

**Off-site:** the folder sits under `/srv`, which the nightly restic job of HOST-02 (03:30 WIB) already copies
encrypted to object storage. The daily backup runs after 02:00 WIB, so each night's off-site copy holds a
consistent per-site archive (the raw MariaDB files restic also copies are not guaranteed consistent).

## Restore

1. A **safety copy** (`before-restore-…`) of the website as it is now is made first. If it fails, nothing changes.
2. The manifest must be this format, complete, and **for this website** (another website's backup is refused).
3. Database: the `wordpress` database is dropped and created empty, then the dump is imported (tables made after
   the backup do not stay behind).
4. Files: unpacked into a new folder `wp.restore-<random>` through `os.Root` (no entry can leave it: `..`, absolute
   paths and writes through links are refused; setuid/setgid bits are dropped). Then web (and php) stop, the
   folders are swapped, the containers are recreated on the new folder, and the old folder is removed.
5. If any step fails, the job names the safety copy to restore.

The panel asks for the domain to be typed before a restore, like a delete. Three safety copies are kept.

## Schedule and space

- Daily backup per website (off by default), keep 1–30 days (default 7). Only daily backups are pruned by the
  keep; manual backups stay until deleted.
- A backup is refused when free disk space is below the website's size plus 256 MB.
- One backup or restore at a time (the agent's change lock, 409 while busy). Progress is shown live on the tab.

## API (nostek-agent, Unix socket only)

`GET /v1/sites/{id}/backups` · `POST /v1/sites/{id}/backups` · `POST /v1/sites/{id}/backups/schedule`
`{"daily":bool,"keep":n}` · `GET /v1/sites/{id}/backups/{name}` (download) · `POST …/{name}/delete` ·
`POST …/{name}/restore` `{"what":"all|files|database"}`. Names must match the pattern above. The audit log
names the backup and what was restored, never its content.

## Tests

Agent unit tests (backup content, restore all / files / database, safety copy, hostile archives, daily schedule
and keep), panel tests (page in both languages, forms, refusals, download relay, audit), harness on real
Docker (real dump, real swap, hostile archive, ownership kept), 7 new mutants.

Left for later: restore to another website (clone, WP-02), upload of a backup made elsewhere (MIG-01),
off-site copy per website on its own schedule.
