# QUOTA-01 — ext4 project quota on the root filesystem

Status: BUILT 2026-11-28 (package `packages/quota-01`). Roadmap slot 15n.

## Why
A project quota limits the disk space of a folder tree whoever owns its files. Website files in Docker belong to
the containers' users (often the same `www-data` for every website), so user quotas cannot tell websites apart; a
project quota can (website folder, database volume, backups). It is the base for disk limits per website group
(GRP-01). A read-only check of the production server (2026-11-28) found ext4 without the `quota` and `project`
features; the decision to turn them on with one reboot was taken the same day.

## How
- The features can only be set while the filesystem is not mounted. APPLY installs an initramfs hook (copies
  `tune2fs` and `e2fsck`) and a `local-premount` one-shot step, rebuilds every initramfs, checks that the step is
  inside the new one (else removes it again) and reboots.
- At boot, before the root filesystem is mounted, the step runs `e2fsck -fy` and then
  `tune2fs -O quota,project -Q usrquota,grpquota,prjquota`. It never stops the boot: a failure is only written to
  `/run/nostek-quota.log` and the server starts as before. Once the features are on it does nothing.
- VERIFY (after the reboot) checks the features, removes the one-shot step and rebuilds the initramfs, installs the
  quota tools, turns enforcement on now (`quotaon -P /`) and at every boot (`prjquota` added to `/` in `/etc/fstab`,
  only now that the feature is on, checked with `findmnt --verify`, the previous fstab put back on a failure), and
  proves it: a test folder with a 1 MB project limit must refuse a 2 MB file.
- REVERT (before the reboot) removes the one-shot step.
- Downtime: one reboot, about 3-10 minutes (filesystem check and quota scan). The provider's web console is the
  way in if a boot ever needs help; a restic backup runs every night.

## Tests
`packages/quota-01/tests/harness.sh` with stub tools in a test root: host and `--ack` guards, PREFLIGHT pass and
fail, APPLY (step written, every initramfs rebuilt, removed again when missing from the initramfs, the filesystem
itself untouched), the boot step itself (check before tune2fs, failed check, failed tune2fs, nothing to do, no root
device: the boot always goes on), VERIFY before and after, REVERT, fstab changed once and only for `/`. CI job
`quota-01` (shellcheck, lock, harness).
