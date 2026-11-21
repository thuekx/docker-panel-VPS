# Website security (SEC-01)

Status: wave 1 [LIVE] v1.23.0 (2026-11-20 22:42 WIB); wave 2 [BUILT] v1.24.0, 2026-11-21. Roadmap slot 12b, after DOM-01. Like the cPanel pages IP Blocker,
Directory Privacy and Hotlink Protection, for one website at a time, on a new **Security** tab.

## Waves (SEC-1)
| Wave | What | Where it runs |
|---|---|---|
| 1 (v1.23.0) | IP blocker, password-protected folders, hotlink protection | the edge (Traefik), so every app and both engines get the same rules |
| 2 (v1.24.0) | custom error pages, MIME types | the website's web server (Nginx/Apache in Docker, host Nginx for native) |
| 3 | web application firewall (Coraza with the OWASP Core Rule Set, off by default), malware scan of the website files (ClamAV), brute-force monitor view | edge plugin / scheduled job |

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

## Limits (known)
- Behind a proxy or CDN in front of the server (for example Cloudflare's orange cloud) the edge sees the proxy's
  address, so the IP blocker cannot tell visitors apart. Block at the CDN in that case.
- Password files live in the edge folder, not in the website's backup. Restoring a backup on the same server keeps
  them; on another server set the passwords again.

## Tests
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
