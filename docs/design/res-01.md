# RES-01 — Resource limits per website

Status: BUILT 2026-11-16 (v1.18.0). Owner 2026-11-16 01:46 WIB: "buatkan sekalian dengan standard default kita dan
hanya bisa dinaikkan jika dibutuhkan" (like cPanel's MultiPHP INI Editor and memory_limit).

## Rules
- Every website runs on **our standard**: the container memory limits of its template (`mem_limit` in the rendered
  compose file) and the standard php.ini (memory_limit 256M, upload_max_filesize/post_max_size 128M,
  max_execution_time 120, max_input_vars 1000). Nothing changes for a website until the owner raises something.
- **Raise only**: memory per container 768 MB, 1 GB, 1.5 GB or 2 GB above the standard; PHP settings from a short
  list each. Setting the standard drops the raise. Below the standard is refused by the agent.
- **memory_limit has room**: raising it raises every container that reads the website's php.ini to at least one
  and a half times memory_limit (the smallest choice that fits), so the owner sets one number.
- **Uploads end to end**: the website's Nginx `client_max_body_size` follows the upload size (Apache has no
  limit below 1 GB; the edge has none).
- **Server view**: the card shows the limits of all websites together (WP-CLI not counted, it runs only for a moment)
  against the server's memory and warns past it. Limits are caps, not reservations: going past is allowed.

## How
- `site.json` keeps only the raises: `ram` (service → MB) and `php_limits`. `Site.Compose()` applies `ram` to the
  rendered file, so every rewrite (WordPress tools, deploys, Update image) keeps them; `keepImages` keeps the images.
- `POST /v1/sites/{id}/limits` writes compose.yml, php.ini and nginx.conf, then `up -d --wait` (with
  `--force-recreate` when php.ini or nginx.conf changed, as those are read at start). On failure the previous files
  and site.json come back and the website starts on them again.

## Tests
Agent unit tests (standard, raise, PHP raises the container, back to standard, refusals, put back on failure,
Nginx body size), API test, panel tests (card in both languages, form, refusal, not-a-number), harness on real
Docker (memory_limit and upload inside PHP, container memory from Docker, Nginx size, refusal, back to standard),
3 mutants.

## Native websites (NATIVE-01)
The same card drives the website's systemd service (`MemoryMax`) and its PHP-FPM pool values.
