# Master roadmap — Nostek Panel

Order is binding (ROADMAP-ORDER-1). Status: ✓ done · ✱ now · ○ next. Times WIB.

| # | Code | What | Owner thread | Status |
|---|---|---|---|---|
| 0 | GOV-00 | Repo polish: MIT licence, README, brand kit, governance files | Rancang panel Docker untuk VPS fresh | ✓ 2026-11-11 |
| 1 | GUIDE-01 | Guide: change the default VPS user + SSH port safely (for other users) | Rancang panel Docker untuk VPS fresh | ✓ 2026-11-11 |
| 2 | HOST-01 | Our VPS: own login user (password) + new SSH port, lock-out safe; user/port/address only in project files | Rancang panel Docker untuk VPS fresh | ✓ 2026-11-11 12:34 WIB (PR #1, RESULT CONFIRM PASS phase=DONE) |
| 3 | HOST-02 | Baseline: updates, firewall, fail2ban, WIB, Docker Engine, backup first (BACKUP-FIRST) | Rancang panel Docker untuk VPS fresh | ✓ done 2026-11-11 13:24 WIB (all steps PASS, restore proven) |
| 4 | PANEL-01 | Edge proxy + panel skeleton with 2FA login at panel.nostek.dev | Rancang panel Docker untuk VPS fresh | ✓ done 2026-11-11 15:06 WIB (VERIFY PASS, cert valid; fixes PR #4, #5); v1.1.0 (THEME-1, VERSION-1) 15:48 WIB |
| 5 | SITE-01 | One-click WordPress, Apache or Nginx per site; first owner domains on preview addresses | Rancang panel Docker untuk VPS fresh | ✓ v1.2.0 live 2026-11-11 16:41 WIB (agent + Websites page, PR #7); first test website next ([site-01.md](../design/site-01.md)) |
| 6 | UI-02 | Live progress without page reloads, Profile page (change password, theme, accent colour), more colourful UI (teal stays default) | Rancang panel Docker untuk VPS fresh | ✓ v1.3.0 live 2026-11-12 01:07 WIB (PR #8) |
| 7 | STACK-01 | Show each website's stack: its containers (web, PHP, database), image and version, state, health, CPU and RAM, restarts; per-container logs and restart (owner 2026-11-12 01:14) | Rancang panel Docker untuk VPS fresh | ○ next (v1.4.0) |
| 8 | SITE-02 | File manager + database admin per site | Rancang panel Docker untuk VPS fresh | ○ |
| 9 | WP-01 | WordPress Tools per site (like WP Toolkit): site card with preview + one-click wp-admin login; tabs Dashboard, Plugins, Themes, Database; updates (core, plugins, themes, autoupdate); security (TLS status, hardening, hotlink protection); tools (PHP version, debug, password protection); performance (search indexing, system cron instead of wp-cron); WordPress integrity check; maintenance mode; clone, copy data, backup, logs, WP-CLI | Rancang panel Docker untuk VPS fresh | ○ (owner 2026-11-11 16:49) |
| 10 | BAK-01 | Per-site backup + restore from the panel | Rancang panel Docker untuk VPS fresh | ○ |
| 11 | MIG-01 | WordPress migration into the panel (plugin / SSH / archive) | Rancang panel Docker untuk VPS fresh | ○ |
| 12 | OPS-01 | Logs, resource usage, cron jobs, readable alerts | Rancang panel Docker untuk VPS fresh | ○ |
| 13 | INST-01 | Public one-command installer for any fresh Ubuntu VPS | Rancang panel Docker untuk VPS fresh | ○ |
