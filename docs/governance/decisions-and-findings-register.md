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
