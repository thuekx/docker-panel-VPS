# Website security (SEC-01)

Status: wave 1 [BUILT] v1.23.0, 2026-11-19. Roadmap slot 12b, after DOM-01. Like the cPanel pages IP Blocker,
Directory Privacy and Hotlink Protection, for one website at a time, on a new **Security** tab.

## Waves (SEC-1)
| Wave | What | Where it runs |
|---|---|---|
| 1 (v1.23.0) | IP blocker, password-protected folders, hotlink protection | the edge (Traefik), so every app and both engines get the same rules |
| 2 | custom error pages, MIME types | the website's web server (Nginx/Apache in Docker, host Nginx for native) |
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

## Limits (known)
- Behind a proxy or CDN in front of the server (for example Cloudflare's orange cloud) the edge sees the proxy's
  address, so the IP blocker cannot tell visitors apart. Block at the CDN in that case.
- Password files live in the edge folder, not in the website's backup. Restoring a backup on the same server keeps
  them; on another server set the passwords again.

## Tests
- Agent unit tests: address checks, every refusal, labels and the native edge file, password file content and
  mode, no hash in site.json, roll-back of password files on a failed job.
- Panel tests: page in both languages and every job state, every form, only a bcrypt hash reaches the agent, no
  hash in the audit log.
- Harness `packages/site-01/tests/security.sh` (CI job `sec-01`): a static website behind a real Traefik edge run
  as uid 65532 with the same file-provider mount as production. 403 for a blocked address on every name and back
  to 200 after unblocking; 401 without a login, 200 with it, a second user and a new password read at once, a
  removed user locked out; 403 for an image with another site's `Referer`, 200 for the website itself, without a
  `Referer`, for an allowed site and for non-media files; nothing left after everything is removed.
