#!/usr/bin/env bash
# shellcheck disable=SC2034  # OUT, RC and BRC are read inside the checks that ok() evaluates
# QUOTA-01 harness: every mode against a test root with stub tools (no real filesystem is changed), and the
# initramfs one-shot step itself run with stubs. Run as root: sudo bash packages/quota-01/tests/harness.sh
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; SRC="$HERE/../quota-01.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
STUB="$T/stub"; mkdir -p "$STUB" "$T/root/etc" "$T/root/boot" "$T/root/run" "$T/root/lib/modules/$(uname -r)/kernel/fs/quota"
touch "$T/root/lib/modules/$(uname -r)/kernel/fs/quota/quota_v2.ko.zst"
printf 'UUID=abc / ext4 defaults 0 1\nUUID=def /boot/efi vfat umask=0077 0 1\n' > "$T/root/etc/fstab"
export QUOTA01_TEST_ROOT="$T/root" STUBDIR="$STUB" PATH="$STUB:$PATH"
PASSES=0; FAILS=0
ok() { if eval "$2"; then echo "ok   $1"; PASSES=$((PASSES+1)); else echo "FAIL $1"; FAILS=$((FAILS+1)); fi; }

# --- stubs: state lives in files under $STUB
echo "nostek-panel" > "$STUB/host"; echo "has_journal ext_attr dir_index extent metadata_csum" > "$STUB/features"; echo 256 > "$STUB/isize"
cat > "$STUB/hostname" <<'EOF'
#!/bin/sh
[ "${1:-}" = -I ] && { echo "10.0.0.5 203.0.113.7 "; exit 0; }; cat "$STUBDIR/host"
EOF
cat > "$STUB/findmnt" <<'EOF'
#!/bin/sh
case "$*" in *--verify*) exit 0 ;; *FSTYPE*) echo ext4 ;; *) echo /dev/vda2 ;; esac
EOF
cat > "$STUB/tune2fs" <<'EOF'
#!/bin/sh
echo "tune2fs $*" >> "$STUBDIR/calls"
[ "$1" = -l ] && { echo "Filesystem features:      $(cat "$STUBDIR/features")"; echo "Inode size:	          $(cat "$STUBDIR/isize")"; exit 0; }
[ -f "$STUBDIR/tune2fs.fail" ] && exit 1; exit 0
EOF
cat > "$STUB/e2fsck" <<'EOF'
#!/bin/sh
echo "e2fsck $*" >> "$STUBDIR/calls"; exit "$(cat "$STUBDIR/e2fsck.rc" 2>/dev/null || echo 0)"
EOF
cat > "$STUB/update-initramfs" <<'EOF'
#!/bin/sh
echo "update-initramfs $*" >> "$STUBDIR/calls"
rm -f "$STUBDIR/initrd"; [ -f "$QUOTA01_TEST_ROOT/etc/initramfs-tools/scripts/local-premount/nostek-quota" ] && [ ! -f "$STUBDIR/initrd.lacks" ] && printf 'usr/sbin/tune2fs\nscripts/local-premount/nostek-quota\n' > "$STUBDIR/initrd"; exit 0
EOF
cat > "$STUB/lsinitramfs" <<'EOF'
#!/bin/sh
echo "usr/bin/busybox"; cat "$STUBDIR/initrd" 2>/dev/null; exit 0
EOF
cat > "$STUB/docker" <<'EOF'
#!/bin/sh
echo /var/lib/docker
EOF
cat > "$STUB/systemctl" <<'EOF'
#!/bin/sh
echo "systemctl $*" >> "$STUBDIR/calls"; exit 0
EOF
cat > "$STUB/quotaon" <<'EOF'
#!/bin/sh
echo "quotaon $*" >> "$STUBDIR/calls"
case "$1" in -Pp) [ -f "$STUBDIR/qon" ] && echo "project quota on / (/dev/vda2) is on" || echo "project quota on / (/dev/vda2) is off" ;; -P) touch "$STUBDIR/qon" ;; esac; exit 0
EOF
cat > "$STUB/setquota" <<'EOF'
#!/bin/sh
echo "setquota $*" >> "$STUBDIR/calls"; [ "$3" = 0 ] && rm -f "$STUBDIR/limit" || touch "$STUBDIR/limit"; exit 0
EOF
cat > "$STUB/chattr" <<'EOF'
#!/bin/sh
echo "chattr $*" >> "$STUBDIR/calls"; exit 0
EOF
cat > "$STUB/dd" <<'EOF'
#!/bin/sh
[ -f "$STUBDIR/limit" ] && [ ! -f "$STUBDIR/nolimit" ] && exit 1; exit 0
EOF
chmod +x "$STUB"/*
run() { : > "$STUB/calls"; OUT="$(bash "$SRC" "$@" --ack QUOTA-01 --expect-ip 203.0.113.7 --expect-host nostek-panel 2>&1)"; RC=$?; }
calls() { cat "$STUB/calls" 2>/dev/null; }
HOOKS="$T/root/etc/initramfs-tools"

# --- guards
OUT="$(bash "$SRC" PREFLIGHT --ack QUOTA-01 --expect-ip 203.0.113.7 --expect-host other 2>&1)"; RC=$?
ok "wrong host: nothing done" '[ "$RC" = 4 ] && grep -q "RESULT QUOTA-01 PREFLIGHT WRONG_HOST" <<<"$OUT"'
OUT="$(bash "$SRC" APPLY --expect-ip 203.0.113.7 --expect-host nostek-panel 2>&1)"; RC=$?
ok "no --ack: refused" '[ "$RC" = 3 ]'

# --- PREFLIGHT
run PREFLIGHT
ok "PREFLIGHT passes on ext4 with 256-byte inodes" '[ "$RC" = 0 ] && grep -q "RESULT QUOTA-01 PREFLIGHT PASS" <<<"$OUT" && ! grep -qE "update-initramfs|e2fsck|-O " <<<"$(calls)"'
echo 128 > "$STUB/isize"; run PREFLIGHT
ok "PREFLIGHT fails with 128-byte inodes" '[ "$RC" = 1 ] && grep -q "inode size 128" <<<"$OUT"'
echo 256 > "$STUB/isize"

# --- APPLY: the one-shot step is checked inside the new initramfs, else removed again
touch "$STUB/initrd.lacks"; run APPLY --no-reboot
ok "APPLY: an initramfs without the step leaves nothing behind" '[ "$RC" = 1 ] && [ ! -e "$HOOKS/hooks/nostek-quota" ] && [ ! -e "$HOOKS/scripts/local-premount/nostek-quota" ]'
rm -f "$STUB/initrd.lacks"; run APPLY --no-reboot
ok "APPLY writes the hook and the one-shot step, rebuilds every initramfs" '[ "$RC" = 0 ] && grep -q "RESULT QUOTA-01 APPLY PASS reboot=now" <<<"$OUT" && [ -x "$HOOKS/hooks/nostek-quota" ] && [ -x "$HOOKS/scripts/local-premount/nostek-quota" ] && grep -q "update-initramfs -u -k all" <<<"$(calls)" && ls "$T/root/var/backups/nostek/quota-01/" | grep -q "^fstab\."'
ok "APPLY never changes the filesystem itself (that happens at boot)" '! grep -qE "^tune2fs -O|^e2fsck" <<<"$(calls)"'
ok "the hook copies tune2fs and e2fsck into the initramfs" 'grep -q "copy_exec /usr/sbin/tune2fs" "$HOOKS/hooks/nostek-quota" && grep -q "copy_exec /usr/sbin/e2fsck" "$HOOKS/hooks/nostek-quota"'

# --- the one-shot step itself (busybox-like sh), with stubs for the initramfs functions and tools
BLK="$(find /dev -maxdepth 1 -type b 2>/dev/null | head -n1)"
if [ -n "$BLK" ]; then
  printf 'resolve_device() { echo %s; }\n' "$BLK" > "$T/functions"
  sed -e "s#/scripts/functions#$T/functions#" -e "s#/usr/sbin/#$STUB/#g" -e "s#/run/nostek-quota.log#$T/boot.log#" "$HOOKS/scripts/local-premount/nostek-quota" > "$T/premount"
  boot() { : > "$STUB/calls"; rm -f "$T/boot.log"; ROOT=UUID=abc sh "$T/premount"; BRC=$?; }
  boot
  ok "boot step: filesystem check first, then quota and project on; boot goes on" '[ "$BRC" = 0 ] && [ "$(grep -vE "^tune2fs -l" "$STUB/calls")" = "$(printf "e2fsck -fy %s\ntune2fs -O quota,project -Q usrquota,grpquota,prjquota %s" "$BLK" "$BLK")" ] && grep -qx done "$T/boot.log"'
  echo 8 > "$STUB/e2fsck.rc"; boot
  ok "boot step: a failed check leaves the features alone and the boot goes on" '[ "$BRC" = 0 ] && ! grep -q "tune2fs -O" "$STUB/calls" && grep -q "skip: e2fsck rc=8" "$T/boot.log"'
  rm -f "$STUB/e2fsck.rc"; touch "$STUB/tune2fs.fail"; boot
  ok "boot step: a failed tune2fs is only logged" '[ "$BRC" = 0 ] && grep -q "^fail: tune2fs" "$T/boot.log"'
  rm -f "$STUB/tune2fs.fail"; echo "has_journal quota extent project metadata_csum" > "$STUB/features"; boot
  ok "boot step: nothing to do once the features are on" '[ "$BRC" = 0 ] && ! grep -q e2fsck "$STUB/calls" && grep -q "done already" "$T/boot.log"'
  printf 'resolve_device() { echo /nonexistent; }\n' > "$T/functions"; boot
  ok "boot step: no root device found, boot goes on" '[ "$BRC" = 0 ] && grep -q "skip: no root device" "$T/boot.log"'
  echo "has_journal ext_attr dir_index extent metadata_csum" > "$STUB/features"
else
  echo "SKIP boot step tests: no block device in /dev"
fi

# --- VERIFY before the features are on: refuses, changes nothing
run VERIFY
ok "VERIFY without the features: FAIL, one-shot step kept" '[ "$RC" = 1 ] && grep -q "quota/project are not on" <<<"$OUT" && [ -e "$HOOKS/scripts/local-premount/nostek-quota" ]'

# --- REVERT before the reboot
run REVERT
ok "REVERT removes the one-shot step and rebuilds the initramfs" '[ "$RC" = 0 ] && [ ! -e "$HOOKS/hooks/nostek-quota" ] && [ ! -e "$HOOKS/scripts/local-premount/nostek-quota" ] && grep -q "update-initramfs -u -k all" <<<"$(calls)"'

# --- after the reboot: VERIFY finishes
run APPLY --no-reboot
echo "has_journal quota extent project metadata_csum" > "$STUB/features"; echo "done" > "$T/root/run/nostek-quota.log"
touch "$STUB/nolimit"; run VERIFY
ok "VERIFY: a limit that does not hold is a FAIL" '[ "$RC" = 1 ] && grep -q "the test limit did not hold" <<<"$OUT"'
rm -f "$STUB/nolimit" "$STUB/qon"; run VERIFY
ok "VERIFY: one-shot step removed, enforcement on, prjquota in fstab, test limit holds" '[ "$RC" = 0 ] && grep -q "RESULT QUOTA-01 VERIFY PASS" <<<"$OUT" && [ ! -e "$HOOKS/scripts/local-premount/nostek-quota" ] && grep -q "quotaon -P /" <<<"$(calls)" && grep -qE "^UUID=abc[[:space:]]+/[[:space:]]+ext4[[:space:]]+defaults,prjquota" "$T/root/etc/fstab" && grep -q "/boot/efi	vfat	umask=0077\|/boot/efi vfat umask=0077" "$T/root/etc/fstab" && grep -q "setquota -P 4190001 1024 1024 0 0 /" <<<"$(calls)" && grep -q "setquota -P 4190001 0 0 0 0 /" <<<"$(calls)"'
run VERIFY
ok "VERIFY twice: prjquota only once" '[ "$RC" = 0 ] && [ "$(grep -o prjquota "$T/root/etc/fstab" | wc -l)" = 1 ]'
run PREFLIGHT
ok "PREFLIGHT after: already on" '[ "$RC" = 0 ] && grep -q "PASS already_on" <<<"$OUT"'

echo "RESULT QUOTA-01 harness pass=$PASSES fail=$FAILS"
[ "$FAILS" = 0 ]
