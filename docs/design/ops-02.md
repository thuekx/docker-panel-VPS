# System page: core services, processes, drift (OPS-02)

Status: LIVE v1.42.0 2026-11-27 13:34 WIB (built 2026-11-27). Roadmap slot 15d (RAYX-2). Settings → System. User guide:
[docs/guides/system.md](../guides/system.md).

## Core services
- **Containers** of the compose project `nostek-core`: edge, WAF, SFTP, the read-only Docker socket proxy and the
  panel. Each shows its Docker state and status text (health included). Any of them can be restarted
  (`docker restart`); the panel's own restart reloads the page.
- **Host units** (systemd state from `systemctl is-active`):
  - Docker: shown only. A restart would stop every website, so it is left to SSH.
  - The agent: restarted by a transient timer (`systemd-run --on-active=3 systemctl restart nostek-agent.service`),
    so the answer reaches the panel first.
  - fail2ban.
  - With the native engine: Nginx and MariaDB.
- The OPS-01 watchdog still restarts unhealthy services by itself. This page is for a look and a restart by hand.
- Each restart is in both audit logs.

## Processes
- `/proc` is read twice, one second apart.
- Kernel threads (no resident memory) are left out.
- Per process:
  - name from `comm`. The command line is never read, since it can hold a password.
  - user (uid looked up)
  - CPU share of one core (ticks / USER_HZ 100 / seconds)
  - resident memory and its share of `MemTotal`
  - where it runs: from `/proc/<pid>/cgroup`, a Docker scope maps to the container name (`docker ps --no-trunc`);
    otherwise the systemd unit is shown.
- The page lists the top 15 by CPU and the top 15 by memory, with the process count, cores and memory. It is read
  only, with no kill button.

## Drift of managed files
- For each ready Docker website, the agent renders what it would write now from the website's settings:
  - `compose.yml`, with the images kept as they run; newer images come only through Update image (IMG-01);
  - `php.ini`;
  - `nginx.conf`, from the template of the website's kind plus the upload size and the web rules;
  - `apache.conf`, when there are rules.
- It compares that with the files on disk. Each file is `same`, `changed` (with a line diff: LCS, at most 60 lines
  shown, a plain line-by-line compare above 3000 lines) or `missing`.
- Native websites are not compared. Their files are owned by the native engine and apt.
- **Write back** (after a confirmation dialog):
  - writes those files and runs `docker compose up -d --wait`, recreating the containers when php.ini or the web
    configuration changed;
  - follows the same path as a change of limits (RES-01): when the website does not start, the files of before come
    back and it is started on them again;
  - needs the server lock (one change at a time) and is in both audit logs.
- `managedFiles(s, fresh)` is shared with RES-01. `fresh = false` (limits) keeps the Nginx body on disk;
  `fresh = true` (drift) starts again from the template.

## API (agent, local)
| Method | Path | What |
|---|---|---|
| GET | `/v1/server/services` | core containers and host units |
| POST | `/v1/server/services/{name}/restart` | restart one (400 for Docker, 404 unknown) |
| GET | `/v1/server/processes` | top processes (about one second) |
| GET | `/v1/server/drift` | drift of every website |
| POST | `/v1/sites/{id}/drift/fix` | write the managed files back |
