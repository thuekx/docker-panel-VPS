# GRP-01 — Website groups

Status: BUILT 2026-11-28 (v1.54.0). Roadmap slot 15m.

## Why
A program built on the panel (EXT-01) may give several websites to one person. Each website already has its own
limits (RES-01), but nothing kept a set of websites together under one ceiling. A group is that ceiling: its
websites share its CPU, memory and process limits, and together they never take more.

## Rules
- A group has a name (1-24 of a-z0-9) and three limits: CPU in percent of one core (10 up to all cores), memory in
  MB (128 up to the server's memory, no swap) and processes/threads (64-65536).
- A website is in at most one group, or in none (the default: nothing changes for it).
- A website's own limits (RES-01) still apply inside the group.
- New limits of a group apply at once to the websites running in it, without a restart. Moving a website into or
  out of a group restarts it there.
- A group with a website cannot be removed.
- Without a program that uses groups, the panel never makes one.

## How
- The agent writes `/etc/systemd/system/npgroup-<name>.slice` (systemd puts it inside `npgroup.slice`), runs
  `systemctl daemon-reload`, `systemctl set-property --runtime` (the running slice takes the new limits now) and
  `systemctl start`. On a failure the previous unit comes back. Groups are kept in `<data>/groups.json`.
- Docker websites: every service of the rendered compose file gets `cgroup_parent: npgroup-<name>.slice`, so every
  rewrite (limits, deploys, Update image, WordPress tools) keeps it. Docker's systemd cgroup driver puts the
  containers inside the slice.
- Native websites: the PHP-FPM service and every WP-CLI run get `Slice=npgroup-<name>.slice`.
- Agent API: `GET /v1/groups`, `PUT /v1/groups/{name}`, `POST /v1/groups/{name}/delete`,
  `POST /v1/sites/{id}/group` (`{"group": ""}` = none). `GET` shows each group's websites and the memory it uses now.
- `panel/ext` Host: `Groups`, `SetGroup`, `DeleteGroup`, `SetSiteGroup` (names checked again by the panel).

## Tests
Agent unit tests (slice unit and live limits, every service in the slice, memory in use, refusals, put back on a
failure, native service and WP-CLI, removal), API test, panel test (the host calls the agent; bad names never reach
it), harness on real Docker and systemd (slice limits from systemd, containers inside the slice, live change,
refused removal, out of the group, removal).
