# NATIVE-01 — Optional native site engine

Status: BUILT v1.20.0 (2026-11-16); scope decided 2026-11-16 01:43 WIB (NATIVE-1): the recommended shape below, PHP 8.1-8.5 from the ondrej/php PPA. ENGINE-1 (2026-11-12): websites run on
Docker stacks only "for now"; a native engine (Nginx/Apache + PHP-FPM on the host, cPanel-style, lighter on RAM) comes
after the core site features, and the add-website page then explains Docker vs native.

## Why

A Docker WordPress site costs about 150-250 MB at rest (its own web server, its own PHP, its own MariaDB). On a
small VPS that limits the number of sites. A native engine shares one web server and one database server, and runs
PHP per site only while it serves requests (`pm = ondemand`): a quiet site costs a few MB.

## Shape (recommended)

| Part | Native engine |
|---|---|
| Web server | One host **Nginx** (Ubuntu 24.04 package) for every native site. Apache stays Docker-only in this wave. |
| PHP | **PHP-FPM 8.1-8.5 from the ondrej/php PPA** (Ubuntu 24.04 itself has only 8.3); one pool per site. |
| Database | One host **MariaDB 10.11** (Ubuntu package), a database and a user per site, unix socket only (no TCP). |
| Apps | **WordPress, plain PHP, static** only. Laravel, Node.js, Go, Python and ready apps stay Docker. |
| Edge | Traefik keeps ports 80/443 and the certificates. Host Nginx listens only on the `nostek-edge` gateway address (port 8088), firewalled to that subnet; the agent writes one Traefik file-provider entry per native site. |
| Folder | The same `/srv/nostek/sites/<id>/` layout (`wp/`, `backups/`, `site.json`), so the File Manager, backups, restores and WordPress Tools keep working. |

### Isolation per site (the part Docker gave for free)
- Each native site gets its own Linux user `np-<id>` (no login shell, no sudo); its files are owned by it, and its
  PHP-FPM pool runs as it. Nginx reads through group membership; other sites cannot read the files.
- Per pool: `open_basedir` to the site folder and its tmp, own `session.save_path` and `upload_tmp_dir`,
  `disable_functions` for process functions (exec, shell_exec, system, passthru, proc_open, popen), memory and
  request limits, `pm = ondemand` with `pm.max_children` from the site's plan.
- Network: an nftables rule set (matching on the site users' uid) blocks site PHP from the agent socket, the panel,
  the Docker API and other sites' ports; outbound internet stays allowed (WordPress updates).
- systemd slice per site (`MemoryMax`, `CPUQuota`, `TasksMax`) as the counterpart of the container limits.

### What changes compared with Docker
- **Updates**: PHP, Nginx and MariaDB patches come from apt (unattended-upgrades, security only), not pinned
  images: IMG-01 "Update image" does not apply to native sites; the Overview shows the installed package versions.
- **Weaker isolation** than a container per site: all native sites share one kernel-level web server, one MariaDB
  server and one PHP build. The add-website page says this plainly and keeps Docker as the default.
- **Switching** a site between Docker and native is a backup and restore into a new site (MIG-01 shares the code).

## Work
1. Host: its own package `packages/native-01/native-01-host.sh` (PREFLIGHT/APPLY/VERIFY/REVERT), so SITE-01 and
   every Docker-only server stay as they are. APPLY installs Nginx, PHP-FPM 8.1-8.5 from the PPA (the distribution
   pools stay off), MariaDB (socket only) and WP-CLI 2.12.0 (sha512 pinned), lets only the edge subnet reach
   `<gateway>:8088` (ufw) and writes `/etc/nostek-agent/native.json`, which is how the agent learns the engine
   exists. REVERT refuses while a native website exists and keeps the packages. The network limits per website come
   from its systemd service (`IPAddressDeny`, `InaccessiblePaths`) instead of a separate nftables rule set.
   Harness: the real install on the CI runner VM.
2. Core: Traefik gets the file provider (`/srv/nostek/edge/dynamic`, read-only) next to the Docker provider.
3. Agent: a second engine behind the same site API (create, start/stop, logs, PHP settings, backups, restore,
   delete, WordPress Tools via WP-CLI as the site user, Database page over the socket).
4. Panel: "Engine: Docker (recommended) / Native" on Add website with the explanation; Overview shows the engine.
5. Harness on a real host (a fresh Ubuntu 24.04 container with systemd is not enough for nftables and slices; the
   CI runner VM is), mutants for isolation (open_basedir off, pool as www-data, firewall rule missing).

