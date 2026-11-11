# HOST-01 — own login user + new SSH port (lock-out safe)

Replaces the provider's default login user with your own user (password login + sudo with password) and moves
SSH to a new port. Built for the project's own VPS; other users should follow
[docs/guides/change-default-user.md](../../docs/guides/change-default-user.md).

Host values (user name, port, server address) are **not** stored in this repository; they are passed as flags
from the private runbook.

## Modes

| Mode | Run as | What it does |
|---|---|---|
| `PREFLIGHT` | current user | Read-only: host identity, Ubuntu 22.04/24.04, sshd, port free, firewall, previous runs. |
| `APPLY` | current user | Backup, create user (password from stdin), SSH on **22 and the new port**, firewall rule, auto-revert timer (10 min), restart SSH, verify listeners. Any failure rolls back at once. |
| `CONFIRM` | new user, **new session on the new port** | Proves the new login works and cancels the auto-revert timer. |
| `FINALIZE` | new user on the new port | Backup, close port 22, lock the old user (password locked, shell nologin, keys and sudoers file moved aside, never deleted), auto-revert timer, verify. Then `CONFIRM` again from a new session. |
| `REVERT` | any sudo user / timer | Restores the files from the last backup (current files moved aside). |
| `STATUS` | any sudo user | Same read-only checks as PREFLIGHT. |

Every run: `sudo bash host-01-user-ssh.sh MODE --ack HOST-01 --user NAME --port N --expect-ip ADDR [...]`.
The wrong host stops at once with `❌ WRONG HOST` on stdout, stderr and the terminal (exit 4).

## Checks

```bash
shellcheck -S warning packages/host-01/host-01-user-ssh.sh packages/host-01/tests/*.sh packages/host-01/tests/stubs/*
sudo bash packages/host-01/tests/harness.sh     # RESULT HOST-01 HARNESS PASS tests=35 sisa_uji=0
sudo bash packages/host-01/tests/mutants.sh     # RESULT HOST-01 MUTANTS PASS killed=8/8 sisa_uji=0
(cd packages/host-01 && sha256sum -c host-01.lock)
```

Fakes are based on stock Ubuntu 24.04 defaults (cloud-init sshd drop-in, socket-activated SSH); the real host
state is confirmed by PREFLIGHT before APPLY.
