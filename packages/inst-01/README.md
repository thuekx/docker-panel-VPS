# INST-01 — one-command installer for a fresh Ubuntu server

Public installer: base, panel and agent in one run. Guide for users: [docs/guides/install.md](../../docs/guides/install.md).
Design: [docs/design/inst-01.md](../../docs/design/inst-01.md).

| Mode | What it does |
|---|---|
| `PREFLIGHT` | Read-only: Ubuntu 22.04/24.04, amd64/arm64, memory ≥ 1 GB, disk ≥ 15 GB, host name, public IPv4, the panel name's A record, the preview wildcard (warning only), ports 80/443/2222 free, Docker/GitHub/Docker Hub reachable, no earlier panel install. |
| `INSTALL` | The same checks, a confirmation (`yes`, or `--yes`), then: the release's commit (newest tag, `--release` or `--src-commit`, at least v1.32.0) fetched with git and the PANEL-01 and SITE-01 scripts checked against their lock files; time zone, updates, swap, Docker Engine (key fingerprint checked), fail2ban and the firewall (every SSH port, 80, 443; 10-minute safety timer); state `inst-01 phase=BASE_DONE`; PANEL-01 APPLY; SITE-01 APPLY; a one-time admin setup link. |

PANEL-01 accepts `inst-01` phase `BASE_DONE` or `DONE` in place of a finished HOST-02 (HOST-02 also proves a
backup; the installer leaves backups for later).

## Checks

```bash
shellcheck -S warning packages/inst-01/nostek-install.sh packages/inst-01/tests/*.sh packages/inst-01/tests/stubs/*
sudo bash packages/inst-01/tests/harness.sh   # RESULT INST-01 HARNESS PASS tests=40 sisa_uji=0
sudo bash packages/inst-01/tests/mutants.sh   # RESULT INST-01 MUTANTS PASS killed=9/9 sisa_uji=0
(cd packages/inst-01 && sha256sum -c inst-01.lock)
```

The harness runs against a fake root with stubbed system commands and a local test repository whose PANEL-01 and
SITE-01 scripts only record how they were called; the real ones have their own harnesses.
