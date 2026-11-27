# Update from the panel (UPD-01)

Status: BUILT v1.38.0, 2026-11-26. Roadmap slot 15 (RAYX-2, CC-FIRST-1). User guide:
[docs/guides/update.md](../guides/update.md).

## Goal
Install a new release of the agent and the panel from **Settings → Update**, without a terminal runbook: a release
list with notes, the release checked before anything changes, a server backup first, live progress, and the
previous release put back by itself when a check fails.

## Shape
- **Panel** (`panel/internal/web/update.go`, `update.html`): the release list with notes (shown as text, never as
  HTML), the running version, a form (release, optional server backup with its passphrase, the account password)
  and the last run. While a run is going, the page polls `/settings/update/status` every 3 s and keeps asking while
  the panel itself restarts; it reloads when the run ends.
- **Agent** (`agent/internal/sites/update.go`, routes `GET/POST /v1/update`):
  - the release list from the GitHub API (no drafts, no pre-releases, only `vX.Y.Z` tags, notes capped at 16 KB),
    cached 10 minutes ("Check again" skips the cache);
  - the commit a tag names from `git ls-remote` (an annotated tag's peeled line wins);
  - with a passphrase, a server backup (BAK-02) first, in the same job; the update starts only when it succeeded;
  - then the **updater of the release that runs now** (`/srv/nostek/src/<commit>/packages/upd-01/nostek-update.sh`,
    checked against `upd-01.lock` first) as its own transient systemd unit (`systemd-run --unit nostek-update-…`),
    so restarting the agent or the panel does not stop it.
- **Updater** (`packages/upd-01/nostek-update.sh RUN`):
  1. takes a lock (`/var/lib/nostek/upd-01/lock`; a second run is refused), checks that SITE-01 and PANEL-01 are
     APPLIED and that the release is newer than the running one;
  2. keeps the panel's database copy (`update.db`, written by the panel with SQLite `VACUUM INTO` just before) as
     `panel.db.before` (root only);
  3. fetches the release by commit (git checks the content), checks that `VERSION` matches the tag and that the
     SITE-01, PANEL-01 and UPD-01 scripts match their sha256 lock files. Nothing has changed until here;
  4. SITE-01 APPLY, then PANEL-01 UPDATE, then SITE-01 VERIFY and PANEL-01 VERIFY, all of the new release, with the
     host name, the public address, the panel name and the preview zone from the agent's configuration.
- **Roll-back.** SITE-01 APPLY and PANEL-01 UPDATE already put their own previous version back when they fail.
  The updater adds the rest: when the panel update fails, the previous agent is installed again (and the panel's
  database copy put back); when a VERIFY fails, the previous panel (PANEL-01 UPDATE with the old commit), the
  panel's database from before the update, and the previous agent are put back, in that order. A run ends as
  `DONE`, `ROLLED_BACK` or `FAILED` (a roll-back that also failed, with the log named).
- **Status.** `/var/lib/nostek/upd-01/status` (phase, tag, from, step, result, times) and `progress` (the updater's
  and the packages' PROGRESS/PASS/WARN/FAIL/RESULT lines). The full output goes to `/var/log/nostek-update.log`
  (0600). No secret is ever printed: the passphrase stays in the agent's memory, in the backup job only.

## First time
The updater has to be on the server before the panel can use it, so **v1.38.0 itself is installed with the
runbook**. From then on, every newer release can be installed from the panel. A server on an older release shows
"not possible on this server yet".

## Security
- Starting an update asks for the account password and is in the audit log (release, from, job, "server backup
  first"); never the passphrase.
- The release list only decides what is offered. What is installed is the commit the tag names in the repository,
  fetched by its id, with every install script checked against its sha256 before anything runs.
- The agent only runs the updater of the release already installed, after checking its sha256, through
  `systemd-run` with an argument list (no shell).

## Tests
- `packages/upd-01/tests/harness.sh` (26): refusals (no ack, tag, commit, same release, unfinished install, second
  run), release checks (VERSION, tampered script, unknown commit: nothing changed), the happy path (order,
  arguments, both on the new commit, database copy, progress, file modes, STATUS), and every failure with its
  roll-back (agent, panel, VERIFY; the database put back; a failed roll-back reported as FAILED).
- Agent: release list (filter, order, notes, cache), not ready (changed or missing updater), refusals before
  anything runs, the systemd-run arguments, a failed start, a running lock, GitHub down, the backup first.
- Panel: page in both languages, notes escaped, the running release not offered, refusals never reach the agent,
  database copies, audit without the passphrase, the running and rolled-back views, the status answer.
