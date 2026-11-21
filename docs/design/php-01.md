# PHP version and Optimize Website (PHP-01)

Status: [BUILT] v1.28.0, 2026-11-21. Roadmap slot 12d, after SFTP-01. Like cPanel's MultiPHP Manager and Optimize
Website, for one website at a time, on a new **Performance** tab (decision PHP-1).

## Change the PHP version
- For WordPress, plain PHP and Laravel websites, on the Docker and the native engine. Static websites, Node.js,
  Go, Python and ready apps run no PHP; the card says so.
- The list shows every PHP version of the catalogue (8.1 marked as without security fixes). A version is shown
  but not offered when:
  - Laravel needs a newer one (Laravel 13: PHP 8.3 or newer, Laravel 12: 8.2);
  - a native website runs on the server's PHP-FPM and that version is not installed (NATIVE-01).
- After a change the card offers **Back to PHP x.y**: the previous version is kept in `site.json` (`prev_php`).
  Going back is a change like any other, with its own backup.

### How it runs (agent job `php`)
1. Docker: the images of the services that follow PHP (WordPress or PHP web server, PHP-FPM, WP-CLI, the base of a
   built image) are pulled while the website keeps running. A failed pull changes nothing. Services whose image
   does not follow PHP (MariaDB, PostgreSQL, Nginx, Redis) keep the image they run; newer ones come only with
   Update image (IMG-01).
2. A backup of kind `before-php` (database and files, kept like the other safety copies).
3. Docker: the new `site.json`, compose file and, for Laravel and PHP on PostgreSQL, the build recipe; the image is
   built again, then `compose up --wait`. Native: the PHP-FPM pool and systemd unit are written for the new
   version and the unit restarted.
4. If the website does not start healthy, the previous files come back and it starts again on its previous PHP;
   if that fails too, the error names the backup to restore.

The website is offline for a few seconds while its containers or its PHP-FPM restart. Website files and the
database are not changed by the agent; a plugin or package that does not support the new version is the reason to
use the way back.

## Optimize Website
Two settings per website, off by default, for every app and both engines. Both are Traefik middlewares on the
website's routers at the edge, written with the other edge rules (SEC-01), so a change is a short job (`optimize`).

- **Compression**: the `compress` middleware on the routers that serve the website (not on the ones that only
  redirect), with a list of text types (HTML, CSS, JavaScript, JSON, XML, SVG, fonts, wasm). Brotli, zstd or gzip
  as the browser asks; answers that are already encoded or smaller than 1 KB are sent as they are.
- **Browser cache**: 1, 7, 30 or 365 days. A router for static extensions (css, js, images, fonts, media, pdf) on
  the main address adds `Cache-Control: public, max-age=…`. It sits below the IP blocker, hotlink protection and
  protected folders (priority 5000), so a blocked or protected file never gets the header. It replaces the
  website's own Cache-Control on those files.

## Tests
- Agent: `php_test.go` (Docker WordPress, Laravel minimum and rebuild, native, pull failure, put back, way back),
  `optimize_test.go` (labels and native edge file).
- Panel: `perf_test.go` (page in both languages, job card, refused versions, optimize change).
- Harness `packages/site-01/tests/security.sh`: a plain PHP website moves from 8.4 to 8.3 and back with a real
  Docker (served version checked), and a static website gets Brotli and the cache header through a real Traefik.
