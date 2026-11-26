# Scheduled server backups and backup destinations (BAK-03)

Status: BUILT v1.41.0, 2026-11-27. Roadmap slot 15c (RAYX-2). Builds on the server backup of BAK-02
([bak-02.md](bak-02.md)). User guide: [docs/guides/server-backup.md](../guides/server-backup.md).

## Schedule
- Off, every day or once a week, at a whole hour (WIB), with a day for weekly. Kept by the agent in
  `<data>/server-backup/schedule.json` (0600, folder 0700).
- **No passphrase is kept** (decision BAK-3, recommended option A built):
  - When the schedule is set, the passphrase becomes an X25519 private key: PBKDF2-SHA256 with a random salt and
    600 000 rounds.
  - The agent keeps only its public key with the salt and the rounds (`npbk.Recipient`).
  - Scheduled backups use npbk layout 2 (`NPBK2`). Each file has a fresh ephemeral X25519 key. The AES-256-GCM key is
    HKDF-SHA256(X25519(ephemeral, recipient), salt = ephemeral public key ‖ recipient public key, info
    `nostek npbk2`). The chunking is the same as layout 1.
  - The reader takes the passphrase, as for layout 1. Every restore path (agent check, installer
    `--restore`, `server-restore`) opens both layouts. The installer's first check accepts both magic strings.
  - Root on the server can read the websites anyway, but stolen copies at a destination, or destination keys, open
    nothing without the passphrase.
- **Who starts it.** The panel owns its database copy (SQLite `VACUUM INTO`, as in BAK-02), so it starts the run:
  1. Every 5 minutes the panel asks `GET /v1/backup-plan`.
  2. When `due` is true, it writes its copy and calls `POST /v1/backup-plan/run`.
  3. The agent checks again that a run is due and records the start. That way one slot runs once, even if the
     call comes twice.
  4. When another job holds the server, the start is given back and the next look tries again.
- Due: inside the chosen hour, on the right day, and not started in this hour yet. The page shows the next time.
- The run is the normal server-backup job (one at a time, the same disk check and local keep 3), followed by the
  step `upload`.

## Destinations
- At most 8, in `<data>/server-backup/dests.json` (0600). The view never returns a secret, only `has_secret`.
  Destinations are not in a server backup.
- **S3**: own client in `agent/internal/s3`, standard library only.
  - Signature V4, checked against the AWS documentation example in the tests.
  - Path-style addresses, so any S3-compatible service works.
  - One PUT up to 64 MB. Above that, a multipart upload with 64 MB parts, each hashed then sent. A failed part is
    sent again twice, then the upload is aborted.
  - Listing uses ListObjectsV2; old copies are removed with DeleteObject.
  - Endpoint https only, no user info or path.
- **SFTP**: the host's OpenSSH client.
  - The agent makes its own ed25519 key once (`ssh-keygen`, `<data>/server-backup/ssh/`), and the page shows the
    public key.
  - At the first test or copy, `ssh-keyscan` pins the host key in a known_hosts file per destination. After that,
    `sftp -b - -F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=yes` refuses a changed host key.
  - A file is put as `.part` and then renamed. Changing the host or port forgets the pinned key.
  - Inputs are checked by pattern (host, user, folder without `..` or quotes); no shell is used.
- After each copy, only files named like a server backup (`nostek-server-YYYYMMDD-HHMMSS.npbk`) count, and the oldest
  beyond Keep (1–90, default 7) are removed. Other files are left alone.
- **Test**: write, list and remove `.nostek-test`. The last test and the last copy (time, result, message, file) are
  kept per destination.
- A listed backup can be sent by hand to one destination (job `server-upload`).

## Alerts and audit
- A failed scheduled backup (reason `backup`) or a failed copy (reason `copy`) adds a watchdog event (service
  `server-backup`, kind `failed`). The panel sends it as the alert kind **backup**.
- Panel audit: `server.backup.schedule`, `server.backup.dest`, `.dest.delete`, `.dest.test`, `server.backup.send`.
  Agent audit: `backup-schedule`, `backup-dest`, `server-backup-scheduled`, and the rest. Neither ever holds the
  passphrase or a secret (tested).
- The schedule and destination forms ask for the account password.

## API (agent, local)
| Method | Path | What |
|---|---|---|
| GET | `/v1/backup-plan` | schedule, `due`, `next`, destinations without secrets, SSH public key |
| POST | `/v1/backup-plan/schedule` | `{every, hour, weekday, passphrase}`; a passphrase is needed the first time it is turned on |
| POST | `/v1/backup-plan/run` | start the due scheduled backup (`{panel_version}`) |
| POST | `/v1/backup-plan/dests` | add or change (`id`); an empty `secret` keeps the stored one |
| POST | `/v1/backup-plan/dests/{id}/delete` · `/test` | remove · test |
| POST | `/v1/backup-plan/ssh-key` | make the SSH key when missing |
| POST | `/v1/server-backups/{name}/send` | `{dest}`: copy one listed backup |

## Not in this step
Password login for SFTP (keys only), virtual-hosted S3 addresses, server-side encryption options, and a restore
straight from a destination: download the file, then restore it as in BAK-02.
