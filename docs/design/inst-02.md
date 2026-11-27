# INST-02: installer for Debian 12 and arm64 (v1.47.0)

Roadmap 15i (RAYX-2). The one-command installer ([inst-01.md](inst-01.md)) now also takes a fresh Debian 12
(bookworm) server, and every part of the panel runs on arm64 (for example Ampere or Graviton servers).

## Systems
| System | amd64 | arm64 | Website engines |
|---|---|---|---|
| Ubuntu 22.04 | ✓ | ✓ (v1.47.0+) | Docker |
| Ubuntu 24.04 | ✓ | ✓ (v1.47.0+) | Docker, native |
| Debian 12 | ✓ (v1.47.0+) | ✓ (v1.47.0+) | Docker |

- Anything else is refused by the system check (for example Debian 11, Rocky Linux).
- The native engine (NATIVE-01: host Nginx, PHP-FPM from the ondrej PPA, host MariaDB) stays Ubuntu 24.04 only; its
  own package refuses other systems, as before. Websites on Debian use the Docker engine.

## What changes in the installer
- The system check sets `DISTRO` (`ubuntu` or `debian`) and `ARCH` (`amd64` or `arm64`).
- Docker Engine comes from `https://download.docker.com/linux/$DISTRO` with the release codename (`bookworm` on
  Debian 12). The same signing key (fingerprint `9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88`) signs both
  repositories, and the installer checks it as before.
- The download check tests the Docker source of this system.
- `python3-systemd` is installed with fail2ban: Debian 12 has no `/var/log/auth.log` by default, and the jail reads
  the systemd journal (`backend = systemd`).
- A missing `curl` (minimal Debian images) is a clear check failure with the command to install it.
- **Oldest release**: on Debian 12 or arm64 the installer takes v1.47.0 or newer (`MIN_INST02`), because older
  releases were not built for them. Ubuntu on amd64 keeps v1.32.0 as its oldest release.

## Images on arm64
- Every pinned image in `agent/internal/catalog` is checked to have an `linux/arm64` build (WordPress, PHP, Nginx,
  Apache, MariaDB, PostgreSQL, Redis, Node, Go, Python, Uptime Kuma, n8n, the WAF, restic and the build images).
- The one exception was `clamav/clamav:1.5.5_base` (amd64 only). On arm64 the malware scanner (SEC-01/SEC-02)
  uses `clamav/clamav-debian:1.5.5_base`, the same ClamAV release on a Debian base, pinned by digest
  (`catalog.ClamAVFor(runtime.GOARCH)`).
- The ionCube loader (PHP-02) already downloads the `aarch64` package on arm64.

## Tests
- `packages/inst-01/tests/harness.sh`: Debian 12 accepted, Debian 11 and other systems refused, arm64 accepted,
  other architectures refused, Debian 12 + arm64 install writes the Debian Docker repository for `arm64` and
  `bookworm`, Ubuntu keeps the Ubuntu repository, `python3-systemd` installed, Debian or arm64 with a release before
  v1.47.0 refused before any change (64 tests).
- `mutants.sh`: any Debian accepted, Debian given the Ubuntu repository and the oldest-release rule removed are all
  caught (16 mutants).
- `catalog_test.go`: `ClamAVFor` gives the Debian image on arm64 and the usual image on amd64.
