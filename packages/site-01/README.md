# SITE-01 — nostek-agent: one-click WordPress sites (Apache or Nginx per site)

Runs after PANEL-01 is applied. Installs **nostek-agent**, the only part of Nostek Panel that may change Docker
(decision SITE-DOCKER-1 = A). The panel keeps read-only Docker access and asks the agent for fixed actions over a
Unix socket. Host values (address, host name, panel domain, preview zone) come from the private runbook.

| Mode | What it does |
|---|---|
| `PREFLIGHT` | Read-only: host identity, PANEL-01 applied, Docker engine + compose, network `nostek-edge`, git, the preview wildcard `*.ZONE` points here (warning only), disk ≥ 5 GB, memory. |
| `APPLY` | Fetches the source at `--src-commit`, builds the agent in Docker (`agent/Dockerfile`) and copies the static binary out, installs `/usr/local/sbin/nostek-agent`, `/etc/nostek-agent/config.json` (640, no secret), `nostek-agent.service` and the daily trash clean-up timer, starts it and checks the socket. Running again with a new commit updates the agent; websites keep running. Any failure restores the previous agent. |
| `VERIFY` | Read-only: service active and enabled, timer active, binary version, config valid, socket `660 root:65532` in a `750` folder, another user cannot call it, raw Docker routes refused, preview wildcard. |
| `REVERT` | Stops the agent and moves its files aside to `/var/backups/nostek/site-01/`. Websites keep running and keep their data; the panel can no longer change them until APPLY runs again. |

## What the agent accepts (nothing else)

`GET /v1/health`, `GET /v1/catalog`, `GET /v1/sites`, `POST /v1/sites/plan`, `POST /v1/sites`, `GET /v1/sites/{id}`,
`POST /v1/sites/{id}/start|stop|restart|delete`, `GET /v1/sites/{id}/logs`, `GET /v1/trash`,
`POST /v1/trash/{entry}/restore`, `GET /v1/jobs/{id}`, `POST /v1/jobs/{id}/secret`.

- Inputs are checked strictly (`agent/internal/spec`): lower-case host names, server `apache|nginx`, PHP from the
  pinned catalogue (`agent/internal/catalog`), plain e-mail, title without quotes or `$`; unknown JSON fields refused.
- A host name is served only when its A record points to this server; otherwise the site starts on its preview
  address `<site-id>.ZONE` and the real domain waits for DNS.
- Images are pinned by digest. PHP 8.1 is offered for migrations only and marked end of life (the official image
  stopped at WordPress 6.9.0 on 2026-01-01).
- The first WordPress admin gets a random password, passed to WP-CLI on stdin (never in process arguments), held only
  in memory and handed to the panel once.
- Every call is written to `/var/log/nostek-agent/audit.log` (600) without secrets.

## Layout on the host

| Path | Content |
|---|---|
| `/srv/nostek/sites/<id>/` | `site.json`, `compose.yml`, `php.ini`, `nginx.conf` (Nginx), `secrets/` (700), `wp/`, `db/`; inside the nightly backup |
| `/srv/nostek/trash/<id>.<unix>/` | deleted sites (700), removed after 7 days by `nostek-agent-purge.timer` |
| `/run/nostek-agent/agent.sock` | the socket (660 root:65532), mounted read-only into the panel |

Each site is its own compose project `np-site-<id>`: database only on the site's internal network; the web server
also on `nostek-edge` (the edge proxy); no published ports. The panel itself is on `nostek-panel`, which sites
cannot reach.

## Checks

```bash
shellcheck -S warning packages/site-01/site-01-agent.sh packages/site-01/tests/*.sh
(cd agent && go vet ./... && go test -race ./...)
sudo bash packages/site-01/tests/harness.sh   # RESULT SITE-01 HARNESS PASS tests=39 sisa_uji=0  (real Docker)
sudo bash packages/site-01/tests/mutants.sh   # RESULT SITE-01 MUTANTS PASS killed=14/14 sisa_uji=0
(cd packages/site-01 && sha256sum -c site-01.lock)
```

## Updating pinned images

Look up the digest of the new tag on Docker Hub, change it in `agent/internal/catalog/catalog.go`, run the checks,
release a new version. Existing sites keep their image until they are recreated.
