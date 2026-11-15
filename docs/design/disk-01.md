# DISK-01 — Disk usage and disk analyzer

Status: BUILT 2026-11-16 (v1.19.0). Owner 2026-11-16 01:51 WIB, from the cPanel screenshots (Files → Disk Usage).

## Website (Disk tab)
- Parts of the website folder: files (`wp/`, what the File Manager shows), database (`db/`), backups, deleted files
  (`files-trash/`, 7 days), other (app releases, settings), and the total.
- Drill-down of any folder of the files: each child with everything below it, largest first (at most 100 rows), and
  the 20 largest files of the website with a link to their folder in the File Manager.
- Walks go through `os.Root` (never out of the website) and never follow a symbolic link; a walk stops after 20 s
  and the page says the sizes are a minimum.

## Server (Disk page)
- The disk that holds `/srv/nostek` (statfs), every website by size, the website trash, and `docker system df`
  (images, containers, volumes, build cache, what can be freed).
- **Remove unused images**: `docker image rm` (never `--force`) of every image that no website's compose file names;
  Docker refuses an image a container uses. Kept: every image of every website (also WP-CLI, which has no running
  container, and images built on the server) and the panel's own images (`nostek/…`: the agent build and the panel of
  this release and the one before, which revert starts again). Removed: older versions kept after Update image (going back then needs
  the before-update backup) and leftovers. Volumes are never offered.
- **Clear build cache**: `docker builder prune --filter until=24h`.
- Both take the agent's change lock, so they never run during a create, update or build.

## Tests
Agent unit tests (parts, drill-down, link not followed, path checks, server view, Docker down, clean-up keeps named
images and in-use images, build cache), API test, panel tests (both pages in both languages, clean-up, refusal),
harness on real Docker (sizes, server view, both clean-ups keep every website running, the WP-CLI image and the panel's own images), 4
mutants.
