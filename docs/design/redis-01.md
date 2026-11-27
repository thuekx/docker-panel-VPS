# REDIS-01: Redis per website (v1.43.0)

Roadmap 15e (RAYX-2). A website gets its own Redis from one switch on its Performance page. A Redis is never shared
between websites.

## What runs
- One more service `redis` in the website's stack (`redis:8.10.3-alpine`, pinned by digest in
  `agent/internal/catalog`). It is only on the website's internal network `back`, so no other website and nothing
  outside can reach it. No port is published.
- `secrets/redis_password` (random, read through the compose secret) and `secrets/redis.conf`:
  - `requirepass`, `maxmemory` (the chosen size) and `rename-command CONFIG ""`;
  - **cache** mode: `allkeys-lru`, no file on disk (`/data` is a tmpfs);
  - **store** mode: `noeviction` and an append-only file in `<site>/redis` (a full Redis answers an error, and no
    queue job is thrown away).
- `mem_limit` of the container is the size plus 64 MB. The size is not added to the RAM limit of the other
  services.
- The healthcheck runs `redis-cli ping` with `REDISCLI_AUTH` read from the secret file. The password never appears
  in compose.yml, argv or a log.
- Sizes: 64, 128, 256 or 512 MB.

## How the website connects
| Website | Connection |
|---|---|
| WordPress | The Redis Object Cache plugin (`redis-cache`, predis client, no PHP extension needed) is installed and turned on. WP-CLI writes `WP_REDIS_HOST`, `WP_REDIS_PORT`, `WP_REDIS_CLIENT`, `WP_REDIS_PREFIX` and `WP_REDIS_PASSWORD`. The password is PHP that reads the secret file at run time, never the password itself. Off: the object cache and the plugin are turned off and the constants are removed. |
| Laravel | `REDIS_HOST`, `REDIS_PORT`, `REDIS_CLIENT=predis`; the password comes through the prepend file from `REDIS_PASSWORD_FILE`. The app needs `predis/predis` and chooses `CACHE_STORE`, `SESSION_DRIVER` and `QUEUE_CONNECTION` itself. |
| plain PHP | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD_FILE`. |
| Node.js, Go, Python | `REDIS_URL`, `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` in the app's environment file (the app must be deployed once first). |

Native websites, static websites and ready apps (Uptime Kuma, n8n) have no Redis.

## The change is a job
Job kind `redis`, holding the change lock:
- **on/change**: `files`, `start redis` (`up -d --wait`), then for WordPress `connect WordPress`;
- **off**: for WordPress `disconnect WordPress`, then `stop redis`. The secret files and the data folder are removed.

When the stack does not start healthy, the old compose.yml and site.json are written back and the website is started
on them again; the job fails with that message.

## Panel
Website → Performance → Redis:
- on/off, mode and memory;
- state, memory used, keys and hit rate (from `INFO`);
- **Empty Redis** (`FLUSHALL`, with a confirmation);
- a hint for each kind of website.

An agent without the Redis routes shows no card.

Agent routes:
- `GET /v1/sites/{id}/redis`
- `POST /v1/sites/{id}/redis` (`{on, mode, mb}`, 202 + job)
- `POST /v1/sites/{id}/redis/flush`

All are audited, the password never.

## Not in this step
- Redis data is not in the website or server backups (cache data is thrown away by design; store data is meant for
  sessions and queues).
- No shared Redis for several websites, and no Redis for native websites.
