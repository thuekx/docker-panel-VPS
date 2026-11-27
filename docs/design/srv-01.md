# Server security page (SRV-01)

Status: BUILT v1.39.0, 2026-11-26. Roadmap slot 15a (RAYX-2). Decision SRV-1 = A. User guide:
[docs/guides/server-security.md](../guides/server-security.md).

## Firewall (ufw)
- The page lists `ufw status numbered`: port, rule, source, comment, IPv6.
- **Add** a rule:
  - allow or deny;
  - a port 1–65535 or a range of at most 1000 ports;
  - TCP, UDP or both (a range needs one protocol);
  - from anywhere, an address or a network.
  - The agent builds the `ufw` argument list itself (no shell) with the comment `nostek panel`.
- **Remove** a rule by its number. The panel also sends the rule as it was listed (`To|Action|From`), so a list that
  changed in between is refused instead of removing another rule.
- **Never from the panel:**
  - Closing an SSH port. The ports come from `sshd -T`, or 22 when that fails. No deny may cover them, and no SSH
    rule may be removed.
  - A deny for every port.
  - Removing the website rules (allow 80/443 from anywhere).
  - Removing the DB-02 database-port rules (comment `nostek db …`). The agent keeps those itself.
- **Trial.**
  - Before a change, the agent copies `/etc/ufw/user.rules` and `user6.rules` to `<data>/firewall/before/` (0700).
  - After the change, a timer puts the copies back and runs `ufw reload` after 10 minutes, unless the change is
    confirmed. **Undo now** does the same at once.
  - Only one change can be on trial at a time.
  - The trial (`trial.json`) survives an agent restart. At start, a trial past its deadline is undone; another one
    is timed again.
  - An undo by the timer is written to the agent's audit log.
- **Confirm from a new session.** The panel enables **Keep the change** only in a session that started after the
  change, so the admin has signed in again (a private window or another browser) and proved they still reach the
  panel. The same check is made on the server when the form is sent.
- Docker's published ports (80, 443, 2222) pass by ufw. The page says so, and rules do not change them.

## fail2ban (SRV-1 = A)
- **Jails.**
  - `fail2ban-client status` and the status of each jail: failing now, banned since start, and the banned
    addresses.
  - Only parsed addresses are shown or sent back.
  - **Unblock** runs `fail2ban-client set JAIL unbanip ADDR`.
- **Never block.**
  - `/etc/fail2ban/jail.d/nostek-allow.local` holds `[DEFAULT] ignoreip = 127.0.0.1/8 ::1 …`.
  - At most 50 entries. Nothing wider than /8 (IPv4) or /16 (IPv6) is accepted.
  - The page shows the visitor's own address as a hint.
  - Saving reloads fail2ban.
- **Panel and SFTP jails.**
  - The panel (login password, unknown e-mail, 2FA code, passkey) and the SFTP server (password) write one line per
    failure: `2026-11-26T15:00:00Z nostek-auth fail ip=203.0.113.9 service=panel`. Only an address parsed by the
    program goes in, so a visitor cannot shape the line. The file is emptied past 1 MB.
  - The panel writes `<data>/panel/auth-fail.log` and SFTP writes `<data>/sftp-state/auth-fail.log`.
  - At start, the agent writes the filter `nostek-auth` and the jails `nostek-panel` and `nostek-sftp`: backend
    `auto` (files; the HOST-02 default is systemd), 10 failures in 10 minutes, ban 1 hour,
    `banaction = iptables-allports[chain=DOCKER-USER]`. It reloads fail2ban only when a file changed.
  - Both services are reached through Docker's published ports. Their traffic goes through FORWARD, not INPUT, so a
    ban in INPUT or ufw would not stop it. A banned address is then refused on every Docker port, including the
    websites, for the hour.
- Every change (rule, confirm, undo, unblock, allow-list) is in the panel's and the agent's audit logs. Firewall and
  allow-list changes ask for the account password.

## Tests
- Agent: ufw output parsing (IPv6, ranges, comments, locks), refusals that never reach ufw (ports, ranges, protocol,
  source text, SSH deny, SSH/web/db rule removal, a changed list), the trial (argument list, a second change refused,
  undo puts the files back, confirm keeps them, delete by number, a change ufw refuses leaves no trial), the timer
  and the resume at start, unblock, the allow-list (normalised, wide networks refused, read back), the jail files
  (written once, reload only on change).
- Panel: page in both languages, SSH rule without a remove button, password checks, the trial view, confirm refused
  from the old session and accepted from a newer one, undo, unblock, allow-list, audit; failed sign-ins in the
  fail2ban file; SFTP writes its line on a wrong password; the log writer drops anything that is not an address.
