---
id: NP-ADM-007
tier: admin
keywords: firewall, ufw, port, fail2ban, blokir, diblok, ip, unblock
---

# Server security: firewall and blocked addresses

**Settings → Server security** shows the server's firewall and the addresses fail2ban has blocked.

## Open or close a port
1. Under **Firewall**, type the port (for example `8080`, or a range like `8000:8100`), choose the protocol, and
   optionally an address or network it is open to (empty = anywhere).
2. Choose **Allow** or **Deny**, type your account password and press **Add rule (on trial)**.
3. The change works at once, but as a **trial for 10 minutes**. To keep it, open the panel in a **new private window**
   (or another browser), sign in, open Settings → Server security and press **Keep the change**. If you do nothing,
   the firewall goes back to how it was by itself.

Removing a rule works the same way (**Remove**, password, then confirm from a new window).

SSH ports are never closed from the panel, and the rules for websites (80, 443) and for database access from outside
are kept. Ports 80, 443 and 2222 are opened by Docker and pass by the firewall.

## Blocked addresses
fail2ban blocks an address for one hour after too many failed sign-ins:
- **SSH**: the server's own jail.
- **Panel sign-in** and **SFTP**: 10 failures in 10 minutes. The address is then blocked from everything on this
  server that runs in Docker, websites included, for that hour.

Press **Unblock** next to an address to lift the block at once.

## Never block
Put the addresses or networks you sign in from under **Never block** (one per line), so a typo never locks you out.
The page shows your current address.
