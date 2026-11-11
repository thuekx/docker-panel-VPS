# HOST-02 — server baseline: WIB, updates, swap, firewall, fail2ban, Docker Engine, backup

Runs after HOST-01. Host values (address, host name, SSH port, S3 endpoint and bucket) come from the private
runbook; keys and the backup password are read from stdin only.

| Mode | What it does |
|---|---|
| `PREFLIGHT` | Read-only: host identity (name + address), Ubuntu 22.04/24.04, SSH port listening, HOST-01 done, disk, reachability of the Docker package source. |
| `APPLY` | Config backup, time zone Asia/Jakarta, apt update/upgrade + unattended security upgrades, 2 GB swap if none, Docker Engine from the official repository (signing key fingerprint verified), daemon log limits, fail2ban on the SSH port, firewall (deny in; allow SSH, 80, 443) with a 10-minute auto-disable. |
| `CONFIRM` | From a NEW SSH session: keeps the firewall and cancels the auto-disable. |
| `BACKUP-SETUP` | Stores S3 credentials root-only (600), creates the encrypted restic repository, nightly timer 03:30 WIB (keep 7 daily, 4 weekly, 6 monthly; weekly check). |
| `BACKUP-TEST` | Test backup + first full backup + restore proof (sha256), then removes the test items (`sisa_uji=0`). Satisfies BACKUP-FIRST. |
| `VERIFY` | WIB, firewall, fail2ban, a test container (removed afterwards), backup timer, phase. |
| `REVERT` | Disables the firewall (also run by the auto-disable timer). Packages and configs stay; they are harmless. |

Note: Docker publishes container ports through its own firewall rules, bypassing ufw. Nostek Panel therefore
only publishes 80/443 from the edge proxy; no other container may publish ports.

## Checks

```bash
shellcheck -S warning packages/host-02/host-02-baseline.sh packages/host-02/tests/*.sh packages/host-02/tests/stubs/*
sudo bash packages/host-02/tests/harness.sh   # RESULT HOST-02 HARNESS PASS tests=32 sisa_uji=0
sudo bash packages/host-02/tests/mutants.sh   # RESULT HOST-02 MUTANTS PASS killed=9/9 sisa_uji=0
(cd packages/host-02 && sha256sum -c host-02.lock)
```
