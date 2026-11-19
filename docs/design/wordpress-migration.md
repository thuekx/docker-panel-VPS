# WordPress migration into Nostek Panel (MIG-01)

Status: [BUILT] v1.21.0, 2026-11-17. Two ways, both in one release, chosen 2026-11-16 23:46-23:49 WIB:
1. **Plugin:** the "Nostek Migrate" plugin on the old site, which the panel pulls from over HTTPS.
2. **cPanel backup:** a cPanel backup restored straight into the website. Most cPanel accounts have no SSH, so this
   replaces the SSH/SFTP idea.

Both share one import core. Roadmap slot MIG-01, after NATIVE-01.

Goal: move an existing WordPress site from an old host into the panel with as few steps as possible. It runs on a
preview address first, before DNS is switched, and the old site keeps running the whole time (read only).

## Flow seen in the panel
1. Make a WordPress website as usual (Docker or native, any PHP) on its preview address.
2. Open its **Migrate** tab and pick a way:
   - **Plugin:** download `nostek-migrate.zip`, install and activate it on the old site (Plugins → Add New →
     Upload). Its page (Tools → Nostek Migrate) shows a **migration code**. Paste the code and press **Start**.
   - **cPanel backup:** upload the backup from the browser, or paste a download link to it. Either a full backup,
     or a home directory backup plus the database backup (`.sql.gz`). A plain zip/tar.gz of the WordPress folder
     with a `.sql` dump also works.
3. The job page shows every step with files and tables done. In the end, it shows the old site's title, version and
   administrators, on the new preview address.
4. Check the preview, then point the domain at the server and press **Check DNS again** on the website's Domains
   tab (DOM-01, v1.22.0): the domain is served and WordPress's addresses move from the preview name to it.

## Way 1: the Nostek Migrate plugin (wave 1)
A single-file PHP plugin (`migrate/nostek-migrate/nostek-migrate.php`, MIT, PHP 7.4+, WordPress 5.6+). It has no
settings and writes nothing except its key. It is served by the panel as a zip built from the repo.

**Key and code**
- On activation it makes a random 32-byte key, stored hashed (sha256) in a non-autoloaded option, with an expiry
  24 hours later.
- Tools → Nostek Migrate shows the code once: `nm1.<base64url(json{u: home URL, k: key})>`. It also has a
  **New code** button and a **Deactivate** reminder.
- Only HTTPS home URLs get a code. Over plain HTTP the page says the old site needs HTTPS first.

**REST routes** (`/wp-json/nostek-migrate/v1/…`)
- Each call needs the header `X-Nostek-Key`, compared with `hash_equals` against the stored hash.
- Expired or wrong keys get 403. After 10 wrong keys in an hour, every call is refused until a new code is made.
- Every route is read only, except `finish`.

| Route | Answer |
|---|---|
| `GET info` | plugin version; WordPress and PHP version; home and siteurl; `$table_prefix`; multisite flag; DB charset; tables with rows and bytes; the file count and bytes under ABSPATH |
| `GET files?after=<path>` | the next 5000 entries under ABSPATH in path order: path, type, size, mtime, mode, and the link target for symbolic links. The order is stable so the panel can resume. |
| `POST files` (`{"paths": [...]}`, up to 1000 files or 64 MB) | a tar stream (ustar, written by the plugin) of those files. Only paths under ABSPATH; `..` and absolute names are refused. |
| `GET file?path=&offset=&length=` | a byte range of one large file (above 64 MB), 32 MB at a time |
| `GET table?name=&after=<pk>&limit=` | SQL for one table: `CREATE TABLE` on the first call, then `INSERT` batches ordered by primary key (keyset, so no slow OFFSET), up to 2000 rows or 8 MB. Values are escaped with the connection's real escape, binary as hex, NULL kept. Tables without a primary key go by `LIMIT … OFFSET`. |
| `POST finish` | deletes the key; the plugin page then says the migration is done and the plugin can be removed |

Shared hosting limits are respected: every answer is bounded so it finishes well inside a 30 second
`max_execution_time`.

**Agent side**
- Reads the code, checks that the home URL is `https://`, and pulls with its own HTTP client:
  - only public addresses: the IP is checked on each connection, so redirects and DNS rebinding cannot reach
    loopback, private or link-local addresses or the server itself;
  - timeouts of 30 s per call, 3 tries with backoff, at most 2 calls at a time (old hosts are often small).
- Files are fetched in batches into `migrate/staging`, then checked against the listed sizes. Large files come in
  ranges.
- Tables stream into `migrate/dump.sql` (0600), filtered as below.
- The key is never logged. The job and audit show only the old host name.

## Way 2: restore from a cPanel backup
**Accepted files**
- A cPanel **full backup** (`backup-<date>_<user>.tar.gz`):
  - the files are under `backup-…/homedir/`, or inside a nested `homedir.tar`, which is read as a stream within
    the stream;
  - the databases are `backup-…/mysql/<db>.sql`.
- A cPanel **home directory backup** (`backup-<domain>-<date>.tar.gz`), plus the database backup from the same page
  (`<db>.sql.gz`) as a second file.
- A plain `.zip`, `.tar` or `.tar.gz` of the WordPress folder, with a `.sql`/`.sql.gz` inside or as a second file.

**Getting the files onto the server**
- **Upload** from the browser, streamed to the agent (`PUT /v1/sites/{id}/migrate/upload?part=archive|database`).
  The files are stored in `migrate/` (0600, outside `wp/`). There is no per-file limit other than 20 GB and the
  free disk (at least 2.5× the size).
- **Download link**: an `https://` address the agent fetches itself, for example the backup file placed for a
  moment under the old site's `public_html`, or a cloud storage link.
  - The address goes through the same public-address guard as the plugin pull.
  - Same size limit; 60 minutes at most.
- Uploaded files are removed after the job, or after a day without a job.

**Finding the website in the archive**
- Every folder holding `wp-includes/version.php` is a candidate. A cPanel account often holds several (addon
  domains).
- With one candidate the job goes on. With several, the job stops before any change and lists them; the panel
  then asks which one, and the job runs again with that folder.
- The database dump: the second uploaded file if there is one. Otherwise the `mysql/<db>.sql` whose name matches
  `DB_NAME` in that folder's `wp-config.php` (read as text, never run), or the only `.sql`/`.sql.gz` in the archive.
- Archive entries go through the backup restore rules:
  - no absolute names or `..`;
  - links stored as links;
  - device files skipped;
  - at most 1 000 000 entries.

## Import core (both ways)
1. **Inspect.** The WordPress root is the folder that holds `wp-includes/version.php`. From the old `wp-config.php`
   (read as text, never run), it reads:
   - `$table_prefix` (letters, digits, `_` only);
   - `MULTISITE`. Multisite is refused in wave 1.
2. **Safety backup**: a full `before-migrate` backup (BAK-01 format, last 3 kept). One press restores the fresh
   website.
3. **Files**:
   - Staged files become the website's own: www-data for Docker, the website user for native.
   - Modes are only rwx; links are kept as links and never followed.
   - The panel's `wp-config.php` is kept (new database login, HTTPS and proxy lines). The old one is saved as
     `wp-config.old.php` for reference, and only the table prefix is carried over (Docker: `WORDPRESS_TABLE_PREFIX`
     in compose, from `site.json`; native: `wp config set table_prefix`).
   - The containers or PHP service are stopped for the swap and started on the new files, as in restore.
4. **Database**:
   - The database is emptied, then the dump is loaded through the existing import path.
   - While streaming, `CREATE DATABASE` / `USE` lines and `DEFINER` clauses are removed.
5. **Addresses**:
   - The old address is read from the imported `home`/`siteurl`.
   - `wp search-replace` runs with `--all-tables-with-prefix --precise --skip-columns=guid`, on `//old-host/path` →
     `//new-host`, then on its JSON-escaped form `\/\/old-host` (serialized data is handled by WP-CLI).
   - Then `http://new-host` → `https://new-host`, and home/siteurl are set to the new address.
6. **Tidy**:
   - `wp cache flush` and `wp rewrite flush`.
   - Caching drop-ins (`advanced-cache.php`, `object-cache.php`) are moved aside with a note, because they point at
     the old host's cache.
   - `wp core verify-checksums` reports but does not fail the job.
7. **Check**:
   - WP-CLI loads the site (`wp option get home` equals the new address);
   - at least one administrator exists;
   - the job names the old and new addresses, the WordPress version and what was moved aside.

   If any step fails, the job says what failed and points to the before-migrate backup.

## Not in wave 1
- Multisite.
- Databases other than MariaDB/MySQL.
- Moving email.
- Switching DNS for you.

## Tests
- Plugin (a PHP script in the harness):
  - wrong, expired and missing keys refused;
  - path escape refused;
  - tar stream readable by Go;
  - table SQL reloads to the same rows, including binary, NULL, emoji and a table without a primary key.
- Agent unit tests:
  - code parse;
  - the URL guard (loopback, private, redirect to private, rebinding through a resolver that changes);
  - the dump filter;
  - the wp-config parse;
  - the search-replace commands;
  - the import core with a fake host.
- Harness (real Docker and the native engine):
  - cPanel layouts built in the harness: a full backup with a nested `homedir.tar` and two WordPress folders (the
    job lists both, then runs with the chosen one), and a home backup with a separate `.sql.gz`;
  - an "old site" WordPress container with the plugin, behind a throwaway HTTPS proxy;
  - migrate it into a new website: title, posts, a serialized option with the new address, the old administrator's
    login, prefix `np_`, the before-migrate backup restores the fresh site;
  - the key is gone after finish.
- Mutants: the URL guard off, search-replace skipped, prefix not carried, no safety backup, plugin key check off.

## As built (v1.21.0)
- Agent routes: `GET /v1/sites/{id}/migrate` (uploaded files and the newest migration job),
  `PUT …/migrate/upload?part=archive|database`, `POST …/migrate/clear`, `POST …/migrate`
  (`{"way":"plugin","code":…}` or `{"way":"backup","link":…,"root":…}`). Code in `agent/internal/sites/migrate.go`
  and `migrate_net.go`.
- Job steps: `download` (link only), `inspect`, `safety database`, `safety files`, `copy files`, `copy database`,
  `load database`, `switch files`, `addresses`, `tidy`, `check`, then `done <summary>`. The job's `progress` field
  says how far the current step is.
- Several WordPress folders in a backup: the job fails with `choose: … choose one: a, b` before any change; the
  panel lists them and runs the job again with `root`.
- The old `wp-config.php` is kept as `wp-config.old.php`, headed by `<?php exit; ?>` so it never runs.
- The copied plugin and its key option are removed on the new site; `finish` deletes the key on the old site.
- A restore puts back the table prefix stored in the backup's `site.json` (finding MIG-01-F1).
- Test harness only: `migrate_test_allow` (networks) and `migrate_test_ca` in the agent config let the harness reach
  its old site on a private Docker network; a real server never sets them, and the agent warns when they are set.
- Panel: tab **Migrate** (`/migrate/{id}`, WordPress websites only), plugin zip at
  `/migrate-plugin/nostek-migrate.zip`, built from `panel/web/plugin/nostek-migrate.php` embedded in the binary.
- Tests: `agent/internal/sites/migrate_test.go`, `agent/internal/api/migrate_test.go`,
  `panel/internal/web/migrate_test.go`, harness `packages/site-01/tests/migrate.sh` (CI job `mig-01`), mutants in
  `packages/site-01/tests/mutants.sh` and the plugin key mutant at the end of the harness.
