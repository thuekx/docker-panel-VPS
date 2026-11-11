# WordPress migration into Nostek Panel (MIG-01) — options

Status: [PROPOSED], roadmap slot MIG-01 (after SITE-02 and BAK-01). Owner asked for it on 2026-11-11.

Goal: move an existing WordPress site from an old host into the panel with as few steps as possible, with a
preview before DNS is switched, and without downtime on the old site.

## Options

| | How | Needs from the old host | Effort | Note |
|---|---|---|---|---|
| **M1. Migration plugin (recommended)** | Install our small "Nostek Migrate" plugin on the old site, copy a one-time code into the panel, click Start. The panel pulls files + database in chunks over HTTPS. | WordPress admin login only | Medium | Works on shared hosting without SSH. Code expires after use. |
| M2. Pull over SSH/SFTP | Panel connects to the old server with an SSH/SFTP login and the DB login, copies files and dumps the database. | SSH/SFTP + DB access | Medium | Fastest for big sites; credentials are used once and never stored. |
| M3. Upload an archive | Upload a `.zip`/`.tar.gz` of the site + `.sql` dump (or a common backup-plugin archive). | A backup file | Low | Fallback for any host; limited by upload size. |

## Common steps (all options)

1. Create the target site (Apache or Nginx) with a temporary preview address.
2. Copy files, import the database, rewrite URLs safely (serialized-data aware search-replace).
3. Health check: home page, admin login, permalinks, media.
4. Owner checks the preview, then switches DNS; the panel issues HTTPS for the real domain.
5. The old site is left untouched (move aside, never delete).

Recommendation: build M1 first with M3 as fallback; add M2 later for large sites.
