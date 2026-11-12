# SITE-02 — File Manager and Database per website (v1.5.0)

Owner ask: cPanel-like file manager and database admin for each website (roadmap #8).

## What the owner gets

| Page | What it does |
|---|---|
| **File Manager** (`/files/<site>`) | Folder view with breadcrumbs; open/edit text files up to 2 MB; upload many files (256 MB each, live progress); download a file or a folder as .zip; extract a .zip in place; new folder, new file; rename/move; permissions; delete to a 7-day file trash with restore. |
| **Database** (`/db/<site>`) | Name, user, host, version, size, tables (rows, data, index); SQL console (≤ 1000 rows per SELECT); export `.sql.gz`; import `.sql`/`.sql.gz` ≤ 512 MB with an automatic copy first (newest 3 kept, downloadable). |

## Security model

- **Only the agent touches files and the database.** The panel streams uploads and downloads through; it keeps nothing.
- **Files: one root per website.** Every call opens the site's `wp/` folder with Go's `os.Root`: no path, `..`, or
  symbolic link (relative or absolute) can leave it. The database files (`db/`), secrets (`secrets/`) and
  `site.json` sit next to `wp/`, so they are out of reach even through a link planted by a hacked WordPress.
  Deletes move the item with `renameat` on open folder handles, so a link is moved as a link, never followed.
- **Zip extraction** refuses links, absolute names and `..` parts, stops at 20 000 entries or 2 GB.
- **Permissions:** rwx bits only (no setuid/setgid/sticky); the owner keeps read+write. New files are `www-data`.
- **Database:** the client runs inside the site's own `db` container as the site's user (`wordpress`); the password
  is read there from the secret file into `MYSQL_PWD`, never in an argument list, log, page or answer.
  SQL text is never written to the audit log (only the statement kind and length).
- **Import is undoable:** a full `mariadb-dump` is taken first; if that copy fails, nothing is imported.
- **CSRF:** normal forms as before; streamed uploads carry the token as the first multipart part, checked before
  any file byte is read.
- **Limits and timeouts:** edge read timeout 30 min (was 60 s); panel and agent lift their short deadlines only for
  transfer routes.

## Tests

Go unit tests (agent `files_test.go`, `db_test.go`, API `files_test.go`; panel `files_test.go`) and the real-Docker
harness (`packages/site-01/tests/harness.sh`): upload served by Nginx and owned by www-data, links to the secret and
the host refused, zip extract, trash and restore, tables, console read and error, export, import and undo by
re-importing the kept copy, password never in the audit log.
