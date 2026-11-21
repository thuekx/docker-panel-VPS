# Operations (OPS-01)

Status: wave 1 [BUILT] v1.29.0, 2026-11-22. Roadmap slot 13, after PHP-01. Decision OPS-1.

- Wave 1 (v1.29.0): visitor statistics and the raw access log (cPanel Metrics), cron jobs (cPanel Cron Jobs).
- Wave 2: Telegram and e-mail alerts, service watchdog, login history of the panel, resource usage over time.

## Visitor statistics (Statistics tab)
- Source: the edge's JSON access log (SEC-01 wave 3), which the agent reads every minute. From v1.29.0 the edge
  also keeps `DownstreamContentSize` and the `Referer` and `User-Agent` request headers; no other header, no
  cookie, no body.
- Per website and WIB day, 31 days: requests, bots (User-Agent of crawlers, scripts or none), visitors (distinct
  addresses that are not bots), bytes sent, answers by class (2xx-5xx), requests per hour, the 300 most visited
  pages (without query), 100 referring sites (other than the website's own names), 100 errors as "code path".
  Addresses of the current day are kept only as short SHA-256 hashes, to count visitors; a closed day keeps only
  its count. Visitors over a period are the daily counts added up.
- The last 500 visits are kept as raw lines in the common log format (address, time, request with query, code,
  size, referrer, browser) and can be downloaded from the page.
- Storage: `<data>/stats/<id>.json` (0600, agent only), written once a minute when something changed; removed
  when the website is deleted for good. The page shows today, 7 or 31 days.

## Cron jobs (Cron tab)
- Up to 20 jobs per website: a schedule (five fields `minute hour day month weekday`, lists, ranges, steps, 0 or 7
  for Sunday, day-of-month OR weekday as in cron; `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`) in WIB and
  one line of command (at most 1000 characters). Stored in `site.json` (`crons`).
- The agent checks every job at the start of each minute and runs the due ones:
  - Docker: `docker compose exec -T` in the website's PHP container (`php` under Nginx, `web` under Apache) as
    www-data (33:33), or in the `app` container of Node.js, Go, Python and ready apps as its own user;
  - native: `systemd-run` as the website's Linux user, in its sandbox, in its document root.
  The command runs with `sh -c` inside that container or sandbox, never in a shell on the host and never as root
  on the server. Static websites have no cron.
- One run of a job at a time (a due time is skipped while the previous run still goes); a run stops after an hour.
- The last 5 runs per job keep the start time, duration, exit code and the last 4 KB of output in
  `<data>/cron/<id>.json` (0600). Run now, turn off/on, edit and delete from the page.
- The audit log names the job and its schedule, never the command (it may carry a token).

## Tests
- Agent: `stats_test.go` (counting, bots, referrers, errors, raw lines, day change, late lines, restart),
  `cron_test.go` (schedule parser, due/next, runs and their record, run now, off, edit, remove, native).
- Panel: `ops_test.go` (both pages in both languages, escaping, raw download, cron changes, static website).
- Harness `packages/site-01/tests/security.sh` with a real Traefik and Docker: the edge log carries only the two
  headers, statistics count the harness's visits, a cron job runs as uid 33 by hand and by its schedule.
