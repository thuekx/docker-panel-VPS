# Server backup and moving to a new server

A **server backup** is one file with the whole panel and every website, sealed with a passphrase. Use it to move a
server to a new VPS, or to keep a full copy before a big change. It is made in the panel and restored by the
installer on the new server.

## What is inside

| Part | What comes back |
|---|---|
| Panel | Admins, two-step login and recovery codes, settings, time zone, alerts (with their sealed secrets), API keys, audit log, login history |
| Each website | Its files, its database, its settings (PHP, server, limits, environment, cron jobs, schedules), domains, security settings, protected-folder passwords, SFTP accounts, deploy keys |

Not inside: older backups of each website, old static releases, the trash, the nightly off-server copy settings
(HOST-02), and the ClouDNS API sub-user for wildcard certificates (set it again on the new server).
A website that was not ready (still being created, or failed) is left out and named in the file.

## Make one

1. **Settings → Server backup**.
2. Type a passphrase twice (at least 12 characters) and your account password, then **Make a server backup**.
3. The websites keep running. Large websites take a while; the page shows the progress and can be left.
4. **Download** the file when it is ready, or copy it from `/srv/nostek/server-backups/` on the server. The newest 3
   are kept.

The passphrase is not stored anywhere. Without it nobody can open the file, the panel included, so keep it in a
password manager. The file is encrypted with AES-256-GCM in 64 KiB parts (key from PBKDF2-SHA256, 600 000 rounds);
a changed, cut or reordered file is refused when it is read.

## On a schedule, with copies off the server (v1.41.0)

**Settings → Server backup → Scheduled server backup**:
1. Choose **Every day** or **Once a week**, the hour (WIB) and, for weekly, the day.
2. Type a passphrase twice (at least 12 characters) and your account password, then **Save the schedule**.
   - The server keeps only a **public key** made from that passphrase, never the passphrase. Scheduled backups are
     sealed to that key, so they need no passphrase at night, and they open on restore with the same passphrase,
     the same way as above (`--restore`).
   - Later changes of the hour or day need no passphrase. A new passphrase applies to the next backups only; older
     files still open with the old one.
3. The newest 3 stay on the server, as for backups made by hand.

**Backup destinations** (same page) receive a copy after every scheduled backup:
- **S3 storage**: endpoint (`https://…`), bucket, an optional folder, access key and secret key. Any S3-compatible
  service with path-style addresses works (IDCloudHost, Wasabi, Backblaze B2, MinIO, AWS). Give the key access to
  that bucket only.
- **SFTP server**: host, port, user and folder. The panel signs in with its own SSH key: press **Make the SSH key**
  and add the public key it shows to `~/.ssh/authorized_keys` of that user. The server's host key is remembered at
  the first test, and a changed host key is refused afterwards.
- Each destination keeps its newest N copies (default 7); older ones there are removed. Other files are left alone.
- **Test** writes and removes a small file. Every destination shows its last copy and its last test.
- **Copy to …** next to a listed backup sends that file by hand.
- Keys and passwords stay in a root-only file on the server and are never shown again. They are not part of a
  server backup: set the destinations again on a new server.

When a scheduled backup or one of its copies fails, an alert of the kind **Backup** is sent (Settings → Alerts).

## Restore on a new server

The new server needs what a normal install needs (see [install.md](install.md)): Ubuntu 22.04/24.04, the panel's
name pointing to it, ports 80/443/2222 free, and no websites yet.

Download and check the installer as in [install.md](install.md), then:

```bash
sudo bash nostek-install.sh INSTALL --domain panel.example.com --restore /root/nostek-server-20261126-170000.npbk
```

- The installer asks for the passphrase (not shown). For a run without a terminal, put the passphrase in a file
  that only root can read and add `--passphrase-file /root/pass`; delete that file afterwards.
- Before it changes anything it checks that the file is a server backup and the passphrase is long enough. After the
  server base (updates, Docker, firewall) it checks the **whole file** (passphrase and every part) before the panel
  is installed. A wrong passphrase stops there; run the same command again with the right one.
- Then it installs the panel and the agent, brings the websites back one by one (settings, then files and
  database), and puts the panel's database in place. No admin link is printed: the admins of the old server sign in
  as before, with their two-step login.
- The last lines list each website: `restored`, `skipped` or `failed`. A failed website does not stop the others;
  its backup is kept in `/srv/nostek/server-restore/<website>/backup.tar.gz`.

## After the restore

1. Sign in and check each website on its preview address.
2. Change each website's DNS to the new server. The certificate is issued as soon as the name points there.
3. Set up the nightly off-server backup again (HOST-02), and open TCP 2222 at the provider if SFTP is used.
4. Keep the old server until the websites answer on the new one.

**Native websites** (host Nginx/PHP-FPM, NATIVE-01) are not brought back automatically: they need their Linux user,
PHP pool and database made on the new server first. Their backup is kept in `/srv/nostek/server-restore/<website>/`.
Create the website again with the same domain and the native engine, copy `backup.tar.gz` into its `backups/` folder
as `manual-YYYYMMDD-HHMMSS.tar.gz`, and restore it from its Backups tab.
