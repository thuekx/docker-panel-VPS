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
│   ├── APP-03 ready-app catalogue: wave 1 Uptime Kuma + n8n ● LIVE v1.17.0 2026-11-16 01:38 WIB; wave 2 Ghost + own MySQL 8 ● LIVE v1.48.0 2026-11-27 13:34 WIB; wave 3 setup lock ● LIVE v1.49.0 2026-11-27 15:34 WIB
│   ├── RES-01 resource limits per website (RAM, PHP INI) ● LIVE v1.18.0 2026-11-16
│   ├── DISK-01 disk usage + disk analyzer ● LIVE v1.19.0 2026-11-16
│   ├── NATIVE-01 optional native site engine ● LIVE v1.20.0 2026-11-16
│   ├── MIG-01 WordPress migration (plugin + cPanel backup) ● LIVE v1.21.0 2026-11-19
│   ├── DOM-01 domains, redirects, DNS check ● LIVE v1.22.0 2026-11-20
│   ├── SEC-01 IP blocker, folder password, hotlink ● LIVE v1.23.0 2026-11-20 (wave 1); error pages, MIME ● LIVE v1.24.0 2026-11-21 (wave 2); malware scan, login attempts ● LIVE v1.25.0 2026-11-21 (wave 3a); WAF ● LIVE v1.26.0 2026-11-21 (wave 3b)
│   ├── EDITOR-1 code editor in the File Manager (CodeMirror 6) ● LIVE v1.24.0 2026-11-21
│   ├── SFTP-01 SFTP per website ● LIVE v1.27.0 2026-11-22 (port 2222)
│   ├── PHP-01 change PHP version, optimize website ● LIVE v1.28.0 2026-11-22
│   ├── OPS-01 logs, usage, cron, alerts, visitor stats .. ✓ wave 1 LIVE v1.29.0 2026-11-23 (statistics + cron); wave 2 LIVE v1.30.0 2026-11-26 (watchdog, alerts, login history, usage)
│   └── SITE-03 static releases, headers, visitor address from a proxy ● LIVE v1.31.0 2026-11-26 (+ result toasts)
└── Distribution
    ├── INST-01 public installer (+ preflight, RAYX-1) .... ● LIVE v1.32.0 2026-11-27 13:34 WIB
    └── after INST-01 (RAYX-1 = A, 2026-11-18)
        ├── API-01 open API with API keys ................ ● LIVE v1.33.0 2026-11-27 13:34 WIB
        ├── BAK-02 panel backup + restore to new VPS ..... ● LIVE v1.34.0 2026-11-27 13:34 WIB
        ├── CERT-01 wildcard + own certificate ........... ● LIVE v1.35.0 2026-11-27 13:34 WIB
        ├── DB-02 remote DB access per IP, table repair .. ● LIVE v1.36.0 2026-11-27 13:34 WIB
        └── AUTH-02 passkey login ........................ ● LIVE v1.37.0 2026-11-27 13:34 WIB
```

Parked after AUTH-02 (RAYX-2, 2026-11-26):

```text
after AUTH-02
├── UPD-01 ● LIVE v1.38.0 2026-11-27 13:34 WIB
├── SRV-01 ● LIVE v1.39.0 2026-11-27 13:34 WIB
├── SEC-02 ● LIVE v1.40.0 2026-11-27 13:34 WIB
├── BAK-03 ● LIVE v1.41.0 2026-11-27 13:34 WIB
├── OPS-02 ● LIVE v1.42.0 2026-11-27 13:34 WIB
├── REDIS-01 ● LIVE v1.43.0 2026-11-27 13:34 WIB
├── CERT-02 ● LIVE v1.44.0 2026-11-27 13:34 WIB
├── PHP-02 ● LIVE v1.45.0 2026-11-27 13:34 WIB
├── TERM-01 ● LIVE v1.46.0 2026-11-27 13:34 WIB
├── INST-02 ● LIVE v1.47.0 2026-11-27 13:34 WIB
├── HELP-01 changelog + guides in the panel ● LIVE v1.50.0 2026-11-27 16:49 WIB
└── WP-02 WordPress: wave 1 safe update ✓ BUILT v1.51.0 2026-11-27; wave 2 staging copy + push to live ✓ BUILT v1.52.0 2026-11-27
```
