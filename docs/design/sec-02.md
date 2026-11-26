# Scheduled malware scans and quarantine (SEC-02)

Status: BUILT v1.40.0, 2026-11-26. Roadmap slot 15b (RAYX-2). Builds on the ClamAV scan of SEC-01
([sec-01.md](sec-01.md)). User guide: [docs/guides/malware-scan.md](../guides/malware-scan.md).

## Schedule per website
- Website → Security → Malware scan has a schedule: **off**, **every night** or **once a week**, and a box
  **move found files to the quarantine by themselves** (off by default).
- Stored in the website's record (`scan_schedule`: `every`, `quarantine`); off removes it.
- The agent's scan loop looks every 10 minutes, only between 01:00 and 05:59 WIB.
  - A website is due when its last scan finished more than its interval ago, minus one hour (23 h or 6 d 23 h), so
    the time of day does not creep later.
  - One scan at a time on the server (the same lock as the manual scan), and only with enough free memory, like the
    manual scan. When it cannot start, the next tick tries again.
- A scheduled scan is marked `scheduled` in the last result shown on the page.

## Quarantine
- A found file can be moved to the quarantine with a press next to it, or by itself when the schedule says so.
- The agent copies it to `<data>/quarantine/q<16 hex>/file` (directory 0700, file 0600, root only) with
  `meta.json`: website, path, signature, size, sha256, mode, owner, time, and whether a scheduled scan moved it.
  Then it removes the file from the website. Files over 256 MB are not moved.
- Only regular files inside the website's own folder; symlinks and paths outside are refused.
- **Put back**: to the same path with its mode and owner; refused when a file is there again.
- **Remove for good**: deletes the copy.
- Files of a website that is deleted stay in the quarantine until removed.
- Settings → Quarantine (administrators) lists every file, newest first. Every move, restore and removal is in the
  audit log; the file itself is never sent to the panel.

## Alert
- A scheduled scan that finds something adds a watchdog event (service `malware`, kind `found`, with the count and
  how many were moved).
- The panel turns it into the alert kind **malware** (Settings → Alerts), sent like the other alerts.
- A manual scan shows its result on the page and sends no alert.

## API (agent, local)
| Method | Path | What |
|---|---|---|
| POST | `/v1/sites/{id}/security/scan-schedule` | `{every, quarantine}`; `every` empty = off |
| POST | `/v1/sites/{id}/quarantine` | `{path, signature}`: move one file |
| GET | `/v1/quarantine` | the list |
| POST | `/v1/quarantine/{qid}/restore` · `/delete` | put back · remove for good |
