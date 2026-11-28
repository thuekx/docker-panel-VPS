# QUOTA-01 — ext4 project quota on the root filesystem

Turns on the ext4 features `quota` and `project` on `/` with one reboot, then enforcement. Design and reasons:
[docs/design/quota-01.md](../../docs/design/quota-01.md).

```bash
sudo bash quota-01.sh PREFLIGHT --ack QUOTA-01 --expect-ip <ip> --expect-host <name>   # read-only
sudo bash quota-01.sh APPLY     --ack QUOTA-01 --expect-ip <ip> --expect-host <name>   # reboots after 10 s
sudo bash quota-01.sh VERIFY    --ack QUOTA-01 --expect-ip <ip> --expect-host <name>   # after the reboot
sudo bash quota-01.sh REVERT    --ack QUOTA-01 --expect-ip <ip> --expect-host <name>   # before the reboot only
```

`quota-01.lock` holds the script's SHA-256; a runbook checks it before running the script.
