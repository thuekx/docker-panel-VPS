# One-command installer (INST-01)

Status: BUILT v1.32.0, 2026-11-26. Decision INST-1 = A (short installer, backups afterwards). Roadmap slot 14 (RAYX-1 = A: preflight of host name, DNS and ports, and a
sha256-checked release). Guide for users: [docs/guides/install.md](../guides/install.md).

## Goal
Anyone with a fresh Ubuntu 22.04/24.04 (or, since INST-02 / v1.47.0, Debian 12; see [inst-02.md](inst-02.md)) server and a DNS name can install Nostek Panel with one command, without the
project's own runbooks. The project's own server keeps its HOST-01/HOST-02/PANEL-01/SITE-01 path.

## Shape
- One script, `packages/inst-01/nostek-install.sh`, two modes: `PREFLIGHT` (read-only) and `INSTALL`.
- It reuses the real install scripts of the release (PANEL-01 APPLY, SITE-01 APPLY, PANEL-01 SETUP-LINK) instead of
  copying them, so the panel is installed exactly as on the project's own server. They run with `--auto` (the
  installer may run from a root shell) and get this server's host name and public address as `--expect-*`.
- Only the base is the installer's own: time zone (optional), updates and unattended upgrades, 2 GB swap when there
  is none, Docker Engine from Docker's repository with the signing key fingerprint checked, fail2ban and ufw.
- State in `/var/lib/nostek/inst-01/state` (`phase` INSTALLING → BASE_DONE → DONE, release, commit). PANEL-01
  accepts `BASE_DONE`/`DONE` there in place of HOST-02 `DONE`. A finished install refuses to run again; a failed one
  can be run again.
- Full output also in `/var/log/nostek-install.log` (0600).

## Checks before anything changes
Ubuntu 22.04/24.04, amd64/arm64, systemd; memory (fail < 1 GB, warning < 2 GB); 15 GB free disk; a usable host
name; the public IPv4 (ipify / ifconfig.me, or `--ip`); an A record from the panel name to that address (the
certificate needs it); the preview wildcard `*.ZONE` (warning only); ports 80/443/2222 free; Docker, GitHub and
Docker Hub reachable; no earlier panel install. `INSTALL` then shows a summary and asks for `yes` (or `--yes`).

## The release
- Default: the newest `vX.Y.Z` tag from `git ls-remote`; or `--release vX.Y.Z`; or `--src-commit SHA`.
- An annotated tag is followed to its commit (`^{}` line). Releases older than v1.32.0 are refused (their PANEL-01
  does not know the installer's base).
- The commit is fetched with git (the id is a hash of the content, git refuses anything else) into
  `/srv/nostek/src/<commit>`, where PANEL-01 and SITE-01 find it again. The VERSION file must match the tag, and the
  PANEL-01 and SITE-01 scripts must match the sha256 in their lock files, before anything on the server changes.
- The installer itself is checked by the user with the sha256 in the README and the guide (from `inst-01.lock`).

## Firewall and SSH
- SSH is not changed (user, port, keys). Every port sshd listens on (`sshd -T`, else `ss`, else 22) stays open in
  ufw and is guarded by fail2ban; 80 and 443 are opened; everything else incoming is closed. Container ports (the
  edge on 80/443, SFTP on 2222) are opened by Docker itself.
- A 10-minute `systemd-run` timer turns ufw off again unless the installer reaches the check that the SSH rules are
  active; then the timer is stopped. A failure while enabling turns ufw off at once.
- Moving SSH to another user and port stays a separate, guided step (HOST-01 guide), because doing it in an
  unattended run can lock a user out.

## Backups
The installer does not set up backups: most new users have no S3 bucket yet. The dashboard shows "No backup result
yet" (no alert fires for a backup that was never set up), and the guide says to set one up before real websites go
on the server. A panel page for the backup target is a later step (BAK-02).

## Tests
- `packages/inst-01/tests/harness.sh` (40): checks and refusals, nothing changed by PREFLIGHT or before consent,
  newest/annotated/old/unknown release, tampered script refused before any change, the arguments PANEL-01 and SITE-01
  get and their order, setup link, time zone, firewall rules and timer, fail2ban ports, Docker key, failures at each
  step, a failed install run again, a second install refused.
- `mutants.sh` (9): script check, Docker key, SSH ports, safety timer, consent, release floor, DNS check, double
  install, log mode.
- PANEL-01 harness: the installer's base is accepted, an unfinished one is not.
