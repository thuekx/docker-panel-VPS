<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/brand/nostek-panel-on-dark.svg">
    <img alt="Nostek Panel" src="assets/brand/nostek-panel-on-light.svg" width="460">
  </picture>
</p>

<p align="center">
  <b>Open-source control panel for Docker on a fresh VPS.</b><br>
  One installer, then everything from the browser: websites, WordPress, files, databases, SSL and backups.
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-0FB5A6"></a>
  <img alt="Status: design" src="https://img.shields.io/badge/status-design%20phase-2EE6C5">
  <img alt="Ubuntu 22.04 / 24.04" src="https://img.shields.io/badge/ubuntu-22.04%20%7C%2024.04-0B1530">
  <img alt="Runs on Docker" src="https://img.shields.io/badge/runs%20on-Docker-0A6E8A">
</p>

---

> **Status:** design phase. Nothing is installable yet. Follow the [roadmap](docs/governance/master-roadmap.md).

## Why Nostek Panel

Classic hosting panels install everything straight onto the server and pick one web server for the whole
machine. Nostek Panel runs every website in its own set of containers, so sites stay isolated and each site can
choose its own stack.

| | |
|---|---|
| 🧩 **One stack per site** | Every website gets its own web server, PHP and database containers. |
| ⚖️ **Apache or Nginx, per site** | Choose the web server when you create a site; neighbours can choose differently. |
| ⚡ **One-click WordPress** | Pick a domain, pick a web server, click Install. SSL is issued automatically. |
| 🗂️ **File manager** | Browse, upload and edit files with a code editor (CodeMirror), limited to the site's own folder. |
| 🗄️ **Database control** | Create databases and users, open a database admin tool from the panel. |
| 🔐 **Secure by default** | Two-factor login, automatic HTTPS, firewall, brute-force protection, no raw Docker socket in the web app. |
| 💾 **Backups** | Encrypted, scheduled backups per site with one-click restore. |
| 🚚 **WordPress migration** | Bring an existing WordPress site from another host: with the Nostek Migrate plugin, or straight from a cPanel backup. |
| 🌐 **Domains** | Extra names, redirects per path, and a DNS and certificate check for every name of a website. |
| 🛡️ **Security** | IP blocker, password-protected folders and hotlink protection at the edge; custom error pages and MIME types per website; malware scan (ClamAV), a login-attempts view with one-press blocking, and a web application firewall (Coraza + OWASP CRS, off by default). |
| 🔑 **SFTP** | SFTP accounts per website on port 2222, locked to the website's folder, with a password or keys; no shell. |
| ⚡ **Performance** | Change the PHP version of a website with a backup first and a one-click way back; compression and browser cache at the edge. |
| 🕒 **Time zone aware** | Every time on screen shows its zone. |

## How it works

```text
                 Internet (80/443)
                        │
              ┌─────────▼─────────┐
              │   Edge proxy      │  routing per domain + automatic HTTPS
              └──┬───────┬─────┬──┘
                 │       │     │
        ┌────────▼──┐ ┌──▼─────────┐ ┌──▼──────────────┐
        │ Panel UI  │ │ Site A     │ │ Site B          │
        │ + API     │ │ Apache+PHP │ │ Nginx + PHP-FPM │
        └────┬──────┘ │ MariaDB    │ │ MariaDB         │
             │        └────────────┘ └─────────────────┘
     restricted Docker API (socket proxy)
```

The panel itself is MIT-licensed. Third-party software (web servers, PHP, databases, WordPress, admin tools) is
pulled as official container images and keeps its own licence. Details: [architecture](docs/design/architecture.md).

## Quick start

Not available yet. The installer will be a single command on a fresh Ubuntu server. Until then:

- [Change the default login user of a new VPS](docs/guides/change-default-user.md) (safe, step by step)
- [Roadmap](docs/governance/master-roadmap.md) · [Decisions](docs/governance/decisions-and-findings-register.md) · [Status](docs/governance/project-status-tree.md)

## Brand

Logo files live in [`assets/brand`](assets/brand) (SVG + PNG). Usage rules: [docs/brand.md](docs/brand.md).

## Contributing and security

- [CONTRIBUTING.md](CONTRIBUTING.md)
- Report vulnerabilities privately: [SECURITY.md](SECURITY.md)
- Project working rules: [INFO.md](INFO.md)

## Licence

[MIT](LICENSE) © 2026 thuekx
