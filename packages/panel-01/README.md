# PANEL-01 — core stack: edge HTTPS proxy, read-only Docker socket proxy, Nostek Panel

Runs after HOST-02 is DONE (baseline, firewall and a proven backup). Host values (address, host name, panel
domain) come from the private runbook. The panel is built on the host from a pinned repository commit; the commit
id is a hash of the content, so the fetched source is checked by `git rev-parse`. Base and service images are
pinned by digest in `panel/Dockerfile` and `deploy/core/compose.yml`.

| Mode | What it does |
|---|---|
| `PREFLIGHT` | Read-only: host identity (name + address), HOST-02 done, Docker engine + compose, git, the panel domain points to this host, ports 80/443 free, disk. |
| `APPLY` | Fetches the source at `--src-commit`, builds `nostek/panel:<commit>`, writes `/srv/nostek/core` (`compose.yml`, `.env` 600), creates data dirs owned by the panel user, starts the stack, waits until healthy, checks that only the edge publishes ports, waits for the HTTPS certificate. Any failure before the certificate step rolls back (stack stopped, previous core files restored). |
| `SETUP-LINK` | Prints a one-time link (30 min) to create the first admin; older links stop working. Refused once an admin exists. |
| `ADMIN-ROTATE` | `--email`: sets a new random admin password, signs out all sessions, prints a one-time link (15 min) that shows the password once (ADMIN-ROTATE-1). The password is never printed. |
| `VERIFY` | Read-only: containers healthy, only the edge publishes ports, the socket proxy refuses writes, HTTP redirects to HTTPS, panel healthy through the edge, certificate valid. |
| `REVERT` | Stops the stack and moves `/srv/nostek/core` aside to `/var/backups/nostek/panel-01/`. Panel data in `/srv/nostek/panel` is kept. |

## Layout on the host

| Path | Content |
|---|---|
| `/srv/nostek/core/` | `compose.yml` (copied from `deploy/core/`), `.env` (600) |
| `/srv/nostek/panel/` | panel database (SQLite), owner 65532, mode 700; inside the nightly backup |
| `/srv/nostek/edge/acme/` | certificates, owner 65532, mode 700 |
| `/srv/nostek/src/<commit>/` | the source the image was built from |
| `/var/lib/nostek/backup/last` | backup status line read by the dashboard (`result=PASS unix=<seconds>`) |

Containers: `edge` (publishes 80→8000 and 443→8443, runs as non-root), `docker-api` (socket proxy on an internal
network, read-only allow-list), `panel` (non-root, read-only root filesystem, no published ports).

## Checks

```bash
shellcheck -S warning packages/panel-01/panel-01-core.sh packages/panel-01/tests/*.sh packages/panel-01/tests/stubs/*
sudo bash packages/panel-01/tests/harness.sh   # RESULT PANEL-01 HARNESS PASS tests=24 sisa_uji=0  (real Docker, ports 80/443)
sudo bash packages/panel-01/tests/mutants.sh   # RESULT PANEL-01 MUTANTS PASS killed=7/7 sisa_uji=0
(cd packages/panel-01 && sha256sum -c panel-01.lock)
```

## Updating pinned images

Find the new digest with `docker buildx imagetools inspect IMAGE:TAG`, change both the tag and the `@sha256:` in
`panel/Dockerfile` or `deploy/core/compose.yml`, and run the harness. Every change goes through a pull request.
