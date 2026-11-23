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

## Service watchdog (wave 2, v1.30.0)
- Docker restarts a container that stops, but not one that keeps running while its health check fails, and nothing
  restarts a native website's PHP or the host's Nginx or MariaDB once systemd has given up. Once a minute the agent
  checks every website's containers (`docker compose ps`; native: `systemctl is-active` of its PHP service), the
  core services `edge`, `panel`, `waf` and `sftp` (one `docker ps` over the `nostek-core` project; the socket proxy
  is left out) and, with the native engine, the host's `nginx` and `mariadb`.
- A service that is unhealthy or not running on three checks in a row is restarted (a website through the same
  path as the Restart button, under the change lock), at most three times an hour; after that it is left alone and
  reported once ("gave up"). A website whose change lock is held (a job is recreating it) is skipped that minute.
- Every restart, failed restart, give-up and recovery is an event in `<data>/watchdog.json` (0600, last 200), read by
  the panel with `GET /v1/watchdog?since=<seq>`.

## Resource usage over time (wave 2)
- Every five minutes the agent takes one `docker stats` over all running containers and adds up CPU and memory per
  website (containers named `np-site-<id>-…`); a native website: `MemoryCurrent` and the `CPUUsageNSec` delta of
  its PHP service. The server: CPU from `/proc/stat`, memory from `/proc/meminfo`, load, and the data disk.
- Kept in `<data>/usage/<id>.json` and `<data>/usage/_server.json` (0600): 24 hours at 5 minutes and 31 days as
  hourly points (CPU averaged, memory and disk at their highest). Shown on the Statistics tab (per hour for today,
  per day for 7 or 31 days, bars against all CPUs and the website's memory limit) and on the Dashboard (the server's
  last 24 hours). Removed with the website's statistics.

## Login history (wave 2)
- Every sign-in attempt to the panel is kept in the panel database (table `logins`, 90 days): time, address,
  User-Agent (200 characters at most), result (signed in, with a recovery code, wrong password, wrong 2FA code,
  account locked). A successful sign-in from an address not used in the 90 days before, by a user who did sign in
  in that time, is marked "new address". The Profile page shows the last 30.

## Alerts by Telegram and e-mail (wave 2)
- Settings page: a Telegram bot (token from @BotFather, chat id) and/or an SMTP account (port 465 TLS or 587/2525
  STARTTLS, never without encryption; sender; up to 5 recipients). The bot token and the SMTP password are stored
  sealed (AES-256-GCM) with `<panel data>/alerts.key` (32 random bytes, 0600, made at the first start); they are
  never shown again, never in the audit log, and errors never carry them (the Telegram address holds the token).
- Kinds, each can be switched off: sign-in from a new address; watchdog events; data disk at its red level (90%);
  memory at its red level (92%) for 5 minutes; nightly server backup failed or older than 36 hours. A condition is
  sent once when it starts and once when it is over. The panel checks once a minute; on its first look it only
  takes the watchdog's position, so old events are not sent again.
- A test button sends one message on every channel and the last 30 alerts are listed with the outcome per channel.

## Tests
- Agent: `stats_test.go` (counting, bots, referrers, errors, raw lines, day change, late lines, restart),
  `cron_test.go` (schedule parser, due/next, runs and their record, run now, off, edit, remove, native).
- Panel: `ops_test.go` (both pages in both languages, escaping, raw download, cron changes, static website).
- Harness `packages/site-01/tests/security.sh` with a real Traefik and Docker: the edge log carries only the two
  headers, statistics count the harness's visits, a cron job runs as uid 33 by hand and by its schedule; a stopped
  web container is restarted by the watchdog within five minutes, and usage is sampled for the website and the server.
- Wave 2: agent `watchdog_test.go` (three checks, three restarts an hour then one give-up, recovery, busy website
  skipped, native PHP, core containers, events kept 0600, usage sums and hourly points); panel `alert_test.go`
  (sealing, token never in an error, no mail without STARTTLS), `store/ops_test.go` (new addresses, 90 days, 200
  alerts), `alerts_test.go` (secrets never shown or audited, test message, kinds, watchdog and memory and backup
  alerts once each, login alert, Profile history, usage tables).
