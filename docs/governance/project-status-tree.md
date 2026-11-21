# Project status tree — Nostek Panel

Updated 2026-11-18 (WIB).

```text
Nostek Panel
├── Governance
│   ├── GOV-00 repo polish, licence, brand kit ............ ✓ (BRAND-1 approved: teal)
│   └── GUIDE-01 change default user guide ................ ✓
├── Host (our VPS, details in project files only)
│   ├── HOST-01 own user + SSH port ........................ ✓ 2026-11-11 12:34 WIB
│   └── HOST-02 baseline + Docker + backup ................ ✓ done 2026-11-11
├── Panel
│   ├── PANEL-01 edge proxy + panel + 2FA ................. ✓ done 2026-11-11
│   ├── SITE-01 one-click WordPress (Apache/Nginx) ........ ✓ v1.2.0 2026-11-11
│   ├── UI-02 live progress, profile, colours ............. ✓ v1.3.0 2026-11-12
│   ├── STACK-01 per-site stack + container view ......... ✓ v1.4.0 2026-11-12
│   ├── SITE-02 file manager + DB admin ................... ✓ v1.5.0 2026-11-12
│   ├── WP-01 WordPress Tools (like WP Toolkit) ........... ✓ v1.6.0 2026-11-12
│   ├── BAK-01 backup / restore ........................... ✓ v1.7.0 2026-11-12
│   ├── APP-01 one-click apps: PHP + static ✓ v1.8.0 2026-11-12; Laravel ✓ v1.9.0/v1.9.1 2026-11-12; APP-02a Node.js ✓ v1.10.0 2026-11-12; APP-02b Go/Python/env ✱ now
│   ├── IMG-01 update image button ● LIVE v1.16.0 2026-11-14 22:29 WIB (PR #23)
│   ├── APP-03 ready-app catalogue: wave 1 Uptime Kuma + n8n ● LIVE v1.17.0 2026-11-16 01:38 WIB; wave 2 Ghost needs MySQL 8 ○
│   ├── RES-01 resource limits per website (RAM, PHP INI) ● LIVE v1.18.0 2026-11-16
│   ├── DISK-01 disk usage + disk analyzer ● LIVE v1.19.0 2026-11-16
│   ├── NATIVE-01 optional native site engine ● LIVE v1.20.0 2026-11-16
│   ├── MIG-01 WordPress migration (plugin + cPanel backup) ● LIVE v1.21.0 2026-11-19
│   ├── DOM-01 domains, redirects, DNS check ● LIVE v1.22.0 2026-11-20
│   ├── SEC-01 IP blocker, folder password, hotlink ● LIVE v1.23.0 2026-11-20 (wave 1); error pages, MIME ● LIVE v1.24.0 2026-11-21 (wave 2); malware scan, login attempts ● LIVE v1.25.0 2026-11-21 (wave 3a); WAF ● LIVE v1.26.0 2026-11-21 (wave 3b)
│   ├── EDITOR-1 code editor in the File Manager (CodeMirror 6) ● LIVE v1.24.0 2026-11-21
│   ├── SFTP-01 SFTP per website ......................... ● BUILT v1.27.0 (port 2222)
│   ├── PHP-01 change PHP version, optimize website ...... ○
│   └── OPS-01 logs, usage, cron, alerts, visitor stats .. ○
└── Distribution
    ├── INST-01 public installer (+ preflight, RAYX-1) .... ○
    └── after INST-01 (RAYX-1 = A, 2026-11-18)
        ├── API-01 open API with API keys ................ ○
        ├── BAK-02 panel backup + restore to new VPS ..... ○
        ├── CERT-01 wildcard + own certificate ........... ○
        ├── DB-02 remote DB access per IP, table repair .. ○
        └── AUTH-02 passkey login ........................ ○
```
