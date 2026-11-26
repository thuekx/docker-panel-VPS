# Install Nostek Panel on a fresh server

One command installs Nostek Panel on a fresh Ubuntu or Debian server: the base (time zone, updates, swap, Docker Engine,
fail2ban, firewall), the panel and its agent. At the end it prints a one-time link to create the first admin.

## Before you start

- A fresh **Ubuntu 22.04 or 24.04** or **Debian 12** server (amd64 or arm64), at least **1 GB memory** (2 GB or more is better) and
  **15 GB free disk**, with root or sudo access over SSH. On Debian 12 and on arm64 the installer takes v1.47.0 or
  newer. Websites run on the Docker engine everywhere; the native engine (host Nginx + PHP-FPM) needs Ubuntu 24.04.
  A minimal Debian may lack curl: `apt-get install -y curl` first.
- A name for the panel, such as `panel.example.com`, with an **A record** to the server's public IPv4 address. The
  HTTPS certificate needs it.
- Optional but useful: a wildcard record `*.preview.example.com` to the same address. A website whose own domain
  does not point here yet gets a preview address under it. By default the preview zone is `preview.` + the panel
  name without its first part; change it with `--preview-zone`.
- Ports **80** and **443** open at your provider (its firewall or security group), and **2222** if you want SFTP.
  Nothing else may use ports 80, 443 or 2222 on the server.

## Install

```bash
curl -fsSLO https://raw.githubusercontent.com/thuekx/docker-panel-VPS/v1.47.0/packages/inst-01/nostek-install.sh
echo "ef9c51dcf31a6d1d8a9d3ead60783231aaec97c3f4b549be52f555b719a16f36  nostek-install.sh" | sha256sum -c -
sudo bash nostek-install.sh PREFLIGHT --domain panel.example.com
sudo bash nostek-install.sh INSTALL --domain panel.example.com --timezone Asia/Jakarta
```

- `PREFLIGHT` only checks and changes nothing: system version and architecture, memory, disk, host name, public address, the
  panel name's DNS, the preview wildcard, free ports and the download sources (Docker, GitHub, Docker Hub).
- `INSTALL` runs the same checks, shows what it will do and asks you to type `yes` (or add `--yes`). It takes
  usually 10 to 20 minutes, most of it building the panel.
- The output is also kept in `/var/log/nostek-install.log` (readable by root only).

Options: `--preview-zone ZONE`, `--timezone Area/City` (default: keep the server's), `--release vX.Y.Z` (default:
the newest release) or `--src-commit SHA`, `--ip ADDRESS` (when the public address cannot be found by itself),
`--yes`.

## What it changes

| Part | What |
|---|---|
| Time zone | Set when `--timezone` is given. |
| Packages | `apt upgrade`, unattended security upgrades, fail2ban, ufw, git. |
| Swap | 2 GB `/swapfile` when the server has no swap. |
| Docker Engine | From Docker's own repository; its signing key fingerprint is checked first. Log limits and live-restore. |
| fail2ban | Guards every port SSH listens on. |
| Firewall (ufw) | Incoming closed except SSH (every port it listens on), 80 and 443. A timer turns the firewall off again after 10 minutes if the install stops before it has checked the rules. Containers that publish a port (the panel's edge on 80/443, SFTP on 2222) are opened by Docker itself. |
| Panel | Core stack in `/srv/nostek/core` (PANEL-01) and the agent `nostek-agent` (SITE-01), built from the release. |

SSH itself is not changed: the same user, port and keys keep working. To move to your own user and another port, see
[change the default login user](change-default-user.md).

## How the release is checked

The installer finds the release's commit from its tag, fetches exactly that commit with git (a commit id is a hash
of its content, so git refuses anything else) and checks the PANEL-01 and SITE-01 install scripts against the
sha256 in the release's own lock files before it runs them. Base and service images are pinned by digest inside the
release. The installer itself is checked by the `sha256sum -c` line above.

## After the install

1. Open the link from the last lines within 30 minutes and create the first admin.
2. **Backups are not set up yet.** The dashboard shows "No backup result yet" until a nightly backup runs. Set up an
   off-server backup before you put real websites on the server.
3. A new admin link later: the installer prints the exact `SETUP-LINK` command at the end.

If a step fails, the output says which one; fix the cause and run `INSTALL` again. A finished install refuses to
run twice.

## Moving a server here

`--restore FILE` installs this server from a server backup of another Nostek Panel (v1.34.0 or newer): the panel,
its admins and every website come back. See [server-backup.md](server-backup.md).
