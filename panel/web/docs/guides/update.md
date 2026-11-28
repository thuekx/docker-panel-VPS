---
id: NP-ADM-003
tier: admin
keywords: update, pembaruan, versi, upgrade, rilis, rollback
---

# Updating Nostek Panel from the panel

From v1.38.0 on, a new release is installed from **Settings → Update**. The agent and the panel move to it
together; websites keep running.

## Update
1. Open **Settings → Update**. The page lists the newer releases with their notes. **Check again** asks GitHub at
   once (the list is otherwise kept for 10 minutes).
2. Choose the release (the newest is chosen already).
3. Keep **Make a server backup first** ticked and type a passphrase twice (at least 12 characters, kept nowhere:
   save it in your password manager). The backup lands under Settings → Server backup like any other.
4. Type your account password and press **Update now**.
5. The page shows the steps as they run. Near the end the panel restarts for a moment; the page keeps asking and
   shows the result by itself.

## When something fails
Every step is checked. If the new agent or panel does not start, or a check (VERIFY) fails afterwards, the
previous release is put back with the panel's database from just before the update, and the page says
**Rolled back** with the reason. The full output is on the server in `/var/log/nostek-update.log`.

If the page says **Failed**, putting the previous release back also failed. The panel may not open; on the server,
`sudo cat /var/log/nostek-update.log` shows what happened, and the server backup made before the update can be
restored.

## "Not possible on this server yet"
The server runs a release from before v1.38.0. Install v1.38.0 once with the update runbook; every later release can
then come from the panel.
