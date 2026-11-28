---
id: NP-ADM-009
tier: admin
keywords: sistem, layanan, service, proses, restart, docker, agen, file dikelola
---

# System page

**Settings → System** shows what Nostek Panel itself runs on.

## Core services
- **Containers**: the edge (HTTPS for every website), the web application firewall, the SFTP server, the read-only
  Docker socket proxy and the panel.
- **Server services**: Docker, the Nostek agent and fail2ban, plus Nginx and MariaDB when native websites are on.

Each one shows whether it runs well. **Restart** restarts one; websites keep running.
- Restarting the panel reloads the page after a few seconds.
- The agent restarts itself a few seconds after you press it.
- Docker is not restarted from here, because that stops every website. Use SSH for it:
  `sudo systemctl restart docker`.

## Processes
The 15 busiest processes by CPU and by memory, measured over one second. **Measure again** takes a new sample. Each
process shows where it runs: a website's container (for example `np-site-voltix-id-php-1`) or a server service.
Only names are shown, never the full command, because a command can hold a password.

## Managed files
For every website the panel writes `compose.yml`, `php.ini` and the Nginx or Apache settings from what you set in
the panel. This part compares them with the files on the server.
- **matches**: nothing to do.
- **differs**: open a file to see the lines (`-` on the server now, `+` what the panel would write). **Write back**
  puts the panel's version in place and restarts the website. If the website does not start with it, the old files
  come back by themselves.

A hand edit made over SSH shows up here. To keep a change for good, make it in the panel (PHP settings, limits,
security rules), because hand edits are lost at the next change from the panel. Native websites are not compared.
