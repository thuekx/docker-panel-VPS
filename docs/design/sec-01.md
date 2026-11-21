# Website security (SEC-01)

Status: wave 1 [LIVE] v1.23.0 (2026-11-20 22:42 WIB); wave 2 [LIVE] v1.24.0 (2026-11-21 14:52 WIB); wave 3a
[BUILT] v1.25.0 (malware scan, login attempts); wave 3b [BUILT] v1.26.0 (web application firewall). Roadmap slot 12b, after DOM-01. Like the cPanel pages IP Blocker,
Directory Privacy and Hotlink Protection, for one website at a time, on a new **Security** tab.

## Waves (SEC-1)
| Wave | What | Where it runs |
|---|---|---|
| 1 (v1.23.0) | IP blocker, password-protected folders, hotlink protection | the edge (Traefik), so every app and both engines get the same rules |
| 2 (v1.24.0) | custom error pages, MIME types | the website's web server (Nginx/Apache in Docker, host Nginx for native) |
| 3a (v1.25.0) | malware scan of the website files (ClamAV), login attempts (brute-force monitor view) | throwaway ClamAV containers / the edge's access log read by the agent |
| 3b (v1.26.0) | web application firewall (Coraza with the OWASP Core Rule Set, off by default) | a WAF service in the core stack that the edge sends a website's visits through |

## Why at the edge
Every visit enters through the edge, whatever the website runs (WordPress, PHP, Node, Go, Python, ready apps) and
whatever its engine (Docker or native). A rule there needs no change inside the website and cannot be undone by a
file in the website's folder (a `.htaccess` can). Each rule is one extra Traefik router with a higher priority
than the website's own routers:

| Rule | Router rule | Priority | Middleware |
|---|---|---|---|
| IP blocker | every served name && `ClientIP(...)` | 30000 | deny (an `ipAllowList` of 127.0.0.2/32, so every visit gets 403) |
| Hotlink protection | main address && media extension && a `Referer` that is not one of the website's names or the allowed sites | 20000 | deny |
| Protected folder | main address && `Path(/f)` or `PathPrefix(/f/)` | 10000 + length | basic auth, then the website's redirects |

Other names of the website keep redirecting to the main address, where the folder asks for the login. The ACME
HTTP challenge runs on the `web` entry point, so no rule can stop certificates; `/.well-known` cannot be protected.

## Rules of each change
| Change | Rule |
|---|---|
| Block | one IPv4/IPv6 address or a network (CIDR), at most /8 (IPv4) or /16 (IPv6) wide; never this server's own address; at most 200 per website; stored normalised (`203.0.113.77/24` → `203.0.113.0/24`). |
| Unblock | by the stored value. |
| Protect / save user | folder like a redirect path (`/[A-Za-z0-9._~/-]`, no `//`, no `..`), `/` protects the whole website; user name 1-32 of `A-Za-z0-9._-`; password 8-72 characters; at most 20 folders and 20 users per folder. A new user is added, an existing one gets the new password. |
| Remove user | not the last one (remove the protection instead). |
| Remove protection | with "sure" ticked; its password file goes too. |
| Hotlink on / save | allowed sites are host names (at most 20); the website's own names are always allowed. Visits without a `Referer` stay allowed (typed addresses, apps, most search engines). Files: jpg jpeg png gif webp avif svg bmp ico mp4 webm mp3. |
| Hotlink off | nothing to do when already off (no job). |

## Passwords
- The panel hashes the password with bcrypt (cost 10). Only the hash goes to the agent; the password is never
  stored, logged or sent anywhere else.
- The agent keeps the hashes only in the edge's password file of the folder,
  `<data>/edge/dynamic/auth/site-<id>-<sha256(folder)[:12]>.htpasswd`, mode 0600, owned by the edge user 65532.
  The edge reads it as `/dynamic/auth/...` (the existing read-only file-provider mount; Traefik ignores files that
  are not YAML or TOML). site.json holds the folder and the user names only, plus a revision number that goes up
  with every password change, so the edge builds the middleware again and reads the new file.
- Audit lines (panel and agent) hold the folder and the user name, never the hash.

## Job
One job `security` with the step `routes`: password files written first (the previous ones kept in memory), then
the routes the same way as DOM-01 (Docker: new labels and `up -d`, a few seconds for the web container; native:
the edge file and an Nginx reload). On a failure the previous password files, routes and site.json come back. A
removed folder's file is deleted only after the routes no longer use it. Removing a website for good from the
trash deletes its password files, unless a website with the same id exists again.

## Wave 2: error pages and MIME types
Only for websites with their own web server: WordPress, plain PHP, Laravel and static (Docker and native). Node,
Go, Python and ready apps answer every request themselves; the page says so.

| Change | Rule |
|---|---|
| Error page on | one of 400, 401, 403, 404, 500, 502, 503, 504. The job writes a starter page `/.nostek-errors/<code>.html` in the website folder (`/public/.nostek-errors/` for Laravel) unless the file is there, then the web server rules. Twice = no job. |
| Error page off | the rule goes; the file stays (it may be turned on again). |
| MIME type add | a content type (`type/subtype`, lower case, no parameters) and 1-20 extensions; an extension belongs to one type only (adding it to another moves it); at most 50 types. Adding to an existing type merges the extensions. |
| MIME type remove | by its type. |

Rules in the web server:
- **Nginx** (Docker and native): a marked block at the end of the server block, rewritten on every change:
  `error_page <code> /.nostek-errors/<code>.html;`, `location ^~ /.nostek-errors/ {}` (wins over the dot-file
  deny), and one full `types {}` block. A `types` block replaces the inherited map and `mime.types` is itself a
  `types` block, so the block holds Nginx's standard map (embedded from Nginx 1.30, minus the website's own
  extensions) plus the website's types (FINDING SEC-01-F2).
- **Apache** (Docker): `apache.conf` with `ErrorDocument` and `AddType`, mounted as
  `/etc/apache2/conf-enabled/zz-nostek.conf` only while there are rules (a mount of a missing file would make Docker
  create a folder).
- Docker websites recreate their web containers when the file changes; native websites check (`nginx -t`) and
  reload the host Nginx. A failure puts the previous files back.
- WordPress and frameworks answer 404 for their own routes through PHP; the pages cover what the web server
  answers itself (missing files, 403, PHP not answering: 502/503/504).

## Wave 3a: malware scan and login attempts

### Malware scan (like cPanel's Virus Scanner)
- Pressed on the Security tab; one scan at a time on the server, and only with at least 1400 MB of memory free
  (clamscan holds every signature in memory, about 1.1 GB). It does not hold the change lock: the website can be
  changed while it runs.
- Step `signatures`: `freshclam` in a throwaway container of the pinned official image
  (`clamav/clamav:1.5.5_base`, no signatures inside) as the image's clamav user (100), with network, no
  capabilities, read-only root, 512 MB. The signatures live in `<data>/clamav/db`; freshclam runs only when they
  are older than 12 hours (ClamAV's mirrors limit how often a server may ask). A failed update with older
  signatures present is a warning on the result; without any it fails the scan.
- Step `scan`: `clamscan` in a throwaway container with **no network**, the website folder mounted read-only, the
  signatures read-only, read-only root, all capabilities dropped except DAC_READ_SEARCH (to read files of other
  owners), 2 GB, 1 CPU, at most 60 minutes. Files over 64 MB are skipped.
- The result is `malware-scan.json` in the website folder (0600): time, files scanned, signatures used, and at most
  500 found files with the signature name. Nothing is changed by the scan. The page offers **Move to file trash**
  (the File Manager's trash, restorable for 7 days) per found file, with "sure" ticked; a removed file shows as
  removed.
- The harness uses a fixed test signature (`clam_test_db` in the agent config, never set on a real server).

### Login attempts (like cPanel's cPHulk history)
- The edge writes an access log (`<data>/edge/logs/access.log`, owned by the edge user): JSON with six fields only
  (address, host, method, path, status, time), no headers, no cookies.
- Every minute the agent reads what is new and keeps only POSTs to login pages: WordPress `wp-login.php` and
  `xmlrpc.php`, and `/login`, `/signin`, `/user/login`, `/administrator`, `/admin/login`, `/auth/login`,
  `/api/login`, `/api/auth/login` (any case, optional closing slash). The host decides the website (every name,
  preview names too); other visits are dropped at once.
- Per website and address: hourly buckets for 7 days, at most 2000 addresses (the least recent go first), in
  `<data>/security/logins.json` (0600) with the read offset, so a restart neither loses nor double counts. Once
  the log is past 32 MB and fully read, the agent empties it (the edge appends, so it keeps writing).
- A failed login is counted where the answer shows it: WordPress shows the form again (200) on a wrong password
  and redirects on a right one; 401, 403, 422 and 429 are refusals. Other answers count as attempts only.
- The page shows the totals of the last 24 hours and 7 days and the 25 busiest addresses with their last try and
  page; **Block** adds an address to the IP blocker (same job as wave 1); blocked addresses are marked. Nothing is
  blocked automatically.
- Until the panel update has given the edge its access log, the card says it is not counting yet.

## Wave 3b: web application firewall (like cPanel's ModSecurity)

### How a visit goes
- A new core service `waf` (container `nostek-core-waf-1`, from the panel image, program `/nostek-waf`, user
  65532, read-only, no capabilities, 1 GB memory) runs Coraza v3.8.1 with the OWASP Core Rule Set v4.25.0 built
  in (Apache-2.0, fetched as Go modules; no download at run time).
- It is a reverse proxy on the `nostek-edge` network (`:8090`, health on `:8091`). While a website's WAF is on,
  every router of that website (Docker labels and the native edge file, security routers too) uses the service
  `site-<id>-waf` from `<data>/edge/dynamic/waf-<id>.yml`, which points at the WAF. The WAF picks the website by
  the Host header and passes the visit to the website itself (`np-site-<id>-web-1:80`, the app container for
  Node/Go/Python and ready apps, or the host Nginx gateway for native websites). An unknown host gets 421.
- Why a proxy and not Traefik's forwardAuth: with forwardAuth the edge would read every body into memory and send
  it twice; through the proxy the first 128 KB of a body are checked and the rest streams on untouched, so large
  uploads still work (decision WAF-1).
- With the WAF off nothing changes for the website: its routers use it directly, the two files are removed.

### Rules per website
- The agent writes `<data>/edge/waf/<id>.json` (0644, read-only for the WAF): hosts, upstream, mode, paranoia
  level, rules switched off, paths skipped. No secret is in it. The WAF reloads a file when it changes; a broken
  file is refused and the previous rules stay.
- Mode **Detect only** (logs, never blocks; the recommended start) or **Block** (403 when the CRS anomaly score is
  reached, rule 949110). Paranoia level 1 (fewest false alarms) or 2.
- **Switch off a rule** by its number (911000-999999; the scoring rules 949xxx and the reporting rules from
  959000 cannot be switched off; at most 50). **Skip a path** (a prefix like `/wp-admin/`; not `/`, no `..`, no
  spaces or quotes; at most 20): visits there are not checked at all.
- Only request headers and the first 128 KB of a body are checked; answers are not read (`SecResponseBodyAccess
  Off`), so pages are not slowed down.
- Turning the WAF on is refused until the panel update has started the WAF service (the agent checks it is
  running and healthy). A turn-on that fails puts the website back as it was. At start the agent writes the rules
  of every website with the WAF on again and removes files of websites that are gone.

### What the page shows
- Every matched rule is a line in `<data>/edge/logs/waf.log` (time, website, address, path, rule, message, the
  matched data cut short, detected or blocked), at most 120 lines per website per minute. The CRS bookkeeping
  rules are not logged.
- The agent reads the log with the login attempts every minute and keeps the last 100 events per website in
  `logins.json`. The page shows the latest 50, how many visits were blocked, and per rule how often it matched
  with a **Switch off** button, so a false alarm is fixed in one press.

## Limits (known)
- Behind a proxy or CDN in front of the server (for example Cloudflare's orange cloud) the edge sees the proxy's
  address, so the IP blocker cannot tell visitors apart. Block at the CDN in that case.
- The WAF checks only the first 128 KB of a body, so an attack hidden after that in a large form is not seen.
  Answers are never checked.
- Password files live in the edge folder, not in the website's backup. Restoring a backup on the same server keeps
  them; on another server set the passwords again.

## Tests
- Wave 3b: WAF unit tests with the real CRS: SQL injection blocked in block mode and passed in detect mode, a
  skipped path, an unknown host (421), XSS in a form body, a 20 MB upload reaching the website whole, events
  logged without bookkeeping rules, switched-off rules letting the visit through, a broken file keeping the
  previous rules, the per-minute event cap, the checks of every field and the order of the directives. Agent:
  refused without the service, every bad input, the labels and native edge file, the rules and edge files, a
  failed turn-on put back, files written again at start and stale ones removed, events and rule counts. Panel:
  the card in both languages, every form and what reaches the agent, audit lines. Harness: the real nostek-waf
  binary next to the real edge: detect mode passes and logs an attack, block mode answers 403 while normal visits
  and forms work, an attack in a form body is blocked, a skipped path, the agent's view within a minute, switching
  off the matched rules, and off again with the files gone.
- Wave 3a: unit tests for the clamscan output, the container options (no network, read-only website, no
  website mount for freshclam), freshclam at most every 12 hours, a failed update with and without older
  signatures, the memory check and one scan at a time; login pages and failure rules, counting per address and
  hour, a half-written line, IPv4-mapped addresses, a host with a port, blocked networks, kept across a restart,
  an emptied log, the log emptied past 32 MB. Panel: both cards in both languages and every scan job state,
  Block, Move to file trash (needs "sure"), audit lines. Harness: the real edge with the core stack's access log
  flags; six POSTs to `/wp-login.php` counted for the edge's address within a minute (a GET is not), blocked from
  the list; a real ClamAV scan with a harness signature finds the planted file and only it, leaves no container,
  and shows it as removed after the move to the file trash.
- Wave 2: unit tests for both servers and native, content-type checks, starter page never overwritten; harness: a
  static website on Nginx (404 and 403 pages, an edited page served, a new and a replaced MIME type, back to the
  standard after removal) and a PHP website on Apache (404 page, MIME type, mount removed); the native-01 harness
  checks the host Nginx.
- Agent unit tests: address checks, every refusal, labels and the native edge file, password file content and
  mode, no hash in site.json, roll-back of password files on a failed job.
- Panel tests: page in both languages and every job state, every form, only a bcrypt hash reaches the agent, no
  hash in the audit log.
- Harness `packages/site-01/tests/security.sh` (CI job `sec-01`): a static website behind a real Traefik edge run
  as uid 65532 with the same file-provider mount as production. 403 for a blocked address on every name and back
  to 200 after unblocking; 401 without a login, 200 with it, a second user and a new password read at once, a
  removed user locked out; 403 for an image with another site's `Referer`, 200 for the website itself, without a
  `Referer`, for an allowed site and for non-media files; nothing left after everything is removed.
