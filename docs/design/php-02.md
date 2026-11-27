# PHP-02: ionCube Loader per website (v1.45.0)

Roadmap 15g (RAYX-2). Some PHP applications are sold encoded with ionCube (billing systems, licensed scripts) and
need the ionCube Loader, a free, closed-source PHP extension. It can now be turned on for one website, from the
Performance page.

## Where the loader comes from
- ionCube publishes one archive per CPU type, with a loader for every PHP version, at
  `https://downloads.ioncube.com/loader_downloads/ioncube_loaders_lin_{x86-64|aarch64}.tar.gz`.
- The agent downloads it over HTTPS **on the server**. It is never in this repository or in an image.
- Download limits: 96 MB and 5 minutes.
- Only the non-thread-safe loaders `ioncube/ioncube_loader_lin_X.Y.so` are kept: the official images are NTS. The
  `_ts` loaders and every other entry are skipped, so no path from the archive is used as a file name.
- The cache is `<data>/cache/ioncube/` with `cache.json`, which holds the time, the archive's sha256, the URL and the
  PHP versions.
- The cache is used for 30 days, then fetched again before it is used for a website. If a refetch fails, the old
  loader is used. A broken download never replaces the cache.
- The sha256 and the versions are shown on the card. ionCube publishes no checksums, so the hash is a record, not a
  pin.

## How a website loads it
- `Site.IonCube` (site.json). The website folder gets `ioncube/ioncube_loader_lin_<php>.so` and
  `ioncube/00-ioncube.ini` (`zend_extension=/usr/local/lib/nostek-ioncube.so`).
- The compose file mounts both, read only, next to the `php.ini` mount of every PHP service:
  - WordPress Apache: `wordpress`;
  - WordPress Nginx: `php`;
  - plain PHP: `web` or `php`;
  - Laravel: `php`.
- `00-` sorts first in `conf.d`, before OPcache, as ionCube requires.
- The official images are unchanged, and nothing is built. The loader moves with the website folder (trash, server
  backup).
- Native websites have no loader: their PHP-FPM is shared by every website of a PHP version. Static websites and
  runtime apps run no PHP.

## The change is a job
Job kind `ioncube`, under the change lock:
- **on**:
  - `loader`: from the cache or a fresh download. Without a loader for the website's PHP, nothing is changed.
  - `restart website`: compose.yml and site.json are written and `up -d --wait` runs; if the website does not start,
    the old files come back.
  - `check`: `php -v` in the PHP container must list "ionCube". Otherwise the website is put back without the
    loader, and the job fails.
- **off**: `restart website` without the mounts, then the `ioncube/` folder is removed.

## PHP change (PHP-01)
- A version the cached archive has no loader for is not offered: "ionCube has no loader for PHP X yet".
- The change copies the loader of the new version before anything else; a failure means nothing changed. The old
  loader stays until the change is done, so putting the previous PHP back still works. Afterwards the other loaders
  are removed.

## Agent API
- `GET /v1/sites/{id}/ioncube`: possible or why, on, PHP, the loader line of `php -v`, the cache, and the newest
  job.
- `POST /v1/sites/{id}/ioncube` with `{on}`: 202 and a job.
