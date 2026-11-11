# Decisions and findings register

Times in WIB. Owner = Thuekx (only decision authority). Thread "Rancang panel Docker untuk VPS fresh" = owner thread
for all rows below unless stated.

| Date (WIB) | Code | Decision | Roadmap slot |
|---|---|---|---|
| 2026-11-11 | **VPS-DETAILS-1 = A** | VPS access details (address, users, SSH port) live only in the private project files, never in this public repo. | all HOST-* packages |
| 2026-11-11 | **LICENSE-1 = MIT** | Repo is public under the MIT licence. | GOV-00 |
| 2026-11-11 11:32 | **PANEL-ARCH-1 = C** | Build our own MIT panel that orchestrates open-source containers (not an existing panel). Apache or Nginx selectable per site. | PANEL-01, SITE-01 |
| 2026-11-11 11:32 | **NAME-1 = Nostek Panel** | Product name chosen by the owner. Variants "Control Panel" / "Docker Panel" exist as logo variants. | GOV-00 |
| 2026-11-11 11:50 | **BRAND-1 = Nostek PANEL, teal** | Logo derived from the NOSTEK N mark (N shape identical in every variant). Main logo = NOSTEK + "PANEL" in the teal colourway; blue kept as alternative. "Docker" not used in the product name (trademark). | GOV-00 |
| 2026-11-11 11:32 | **MIG-1** | WordPress migration from an old host into the panel goes on the roadmap (options in docs/design/wordpress-migration.md). | MIG-01 |
| 2026-11-11 11:33 | **USER-1** | On our own VPS the provider default user is replaced by our own login user with a password (name and port kept in project files). This change is for our VPS only; other users get a written guide instead of a package. | HOST-01, GUIDE-01 |
| 2026-11-11 11:50 | **USER-1 detail** | Our login user logs in over SSH with a password and uses sudo with a password; owner confirms the provider firewall allows the new SSH port and the panel A record points to the VPS. | HOST-01 |
| 2026-11-11 12:35 | **BACKUP-1 = A** | Server backups go encrypted (restic) to S3-compatible object storage at a different provider than the VPS. Provider, endpoint and bucket: waiting for owner; keys entered hidden on the host only. | HOST-02 |
| 2026-11-11 12:35 | **BACKUP-1 detail** | Reuse the S3 object storage account the owner already uses in another project (REUSE-FIRST); create a new bucket for this server only. | HOST-02 |
| 2026-11-11 12:56 | **PANEL-STACK-1 = A + animation** | Panel in Go with server-rendered HTML (SQLite first). UI animation with Motion (motion.dev, MIT) and anime.js (MIT), vendored and pinned in the repo (no CDN at runtime), respecting reduced-motion settings. | PANEL-01 |
| 2026-11-11 14:10 | **FINDING PANEL-01-F1** | First host APPLY failed: the reachability check demanded a valid certificate before ACME had issued one; rollback worked. Fixed in PR #4 with a harness test on the production certificate path. Lesson: test hooks must not skip the path production takes. | PANEL-01 |
| 2026-11-11 14:46 | **FINDING PANEL-01-F2** | 2FA QR code not shown: html/template blocks plain-string data URLs. Fixed in PR #5 (template.URL, secret renewed on each enrol visit so a key seen in a screenshot never becomes active). Lesson: check rendered images in UI screenshots, not only text. | PANEL-01 |
| 2026-11-11 15:06 | **THEME-1 (owner)** | Panel gets a light/dark theme setting (Light / Dark / Follow system), with the next package. | next package |
| 2026-11-11 15:08 | **VERSION-1 (owner)** | The panel shows release versions (`v1.0.0`, `v1.1.0`), never commit ids; `CHANGELOG.md` and git tags per release. | all |
| 2026-11-11 15:13 | **SITE-PHP-1 (owner)** | PHP versions offered: 8.1 up to the latest supported release; 8.1 labelled end-of-life. | SITE-01 |
| 2026-11-11 15:31 | **MAIL-1 (owner)** | No mail server for now. | — |
| 2026-11-11 15:30 | **EDITION-1 (owner)** | Nostek Panel stays MIT and fully usable on its own. Paid add-ons are separate products and are not in this repository; nothing here depends on them. | all |
| 2026-11-11 15:43 | **SITE-DOCKER-1 = A (owner)** | Docker changes go through a separate host agent with fixed, checked actions (option A in [site-01.md](../design/site-01.md)); the panel itself never gets Docker write access. | SITE-01 |
| 2026-11-11 15:48 | **v1.1.0 released** | Updated on the owner's server with `panel-01 UPDATE` (no REVERT), VERIFY PASS, certificate valid. THEME-1 and VERSION-1 done. | PANEL-01 |
| 2026-11-11 16:41 | **v1.2.0 released** | `site-01` PREFLIGHT, APPLY and VERIFY PASS on the owner's server (agent v1.2.0, preview DNS ok), then `panel-01 UPDATE` PASS (certificate valid). Websites page live. | SITE-01 |
