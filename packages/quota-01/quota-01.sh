#!/usr/bin/env bash
# Nostek Panel - QUOTA-01: turn on ext4 project quota on the root filesystem (one reboot).
# A project quota limits the disk space of a folder tree whoever owns its files: the base for disk limits per website
# group. The ext4 features "quota" and "project" can only be set while the filesystem is not mounted, so APPLY puts a
# one-shot step into the initramfs that sets them at the next boot, before the root filesystem is mounted, and
# reboots. VERIFY checks the features, removes the one-shot step, installs the quota tools, turns enforcement on
# (now and in /etc/fstab) and proves a limit with a test folder.
# Modes: PREFLIGHT (read-only) | APPLY (reboots) | VERIFY | REVERT (before the reboot: removes the one-shot step)
# Usage: sudo bash quota-01.sh MODE --ack QUOTA-01 --expect-ip ADDR --expect-host NAME
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="QUOTA-01"
R="${QUOTA01_TEST_ROOT:-}"
HOOK="$R/etc/initramfs-tools/hooks/nostek-quota"
PREMOUNT="$R/etc/initramfs-tools/scripts/local-premount/nostek-quota"
STATE_DIR="$R/var/lib/nostek/quota-01"
BACKUP_DIR="$R/var/backups/nostek/quota-01"
LOG="$R/run/nostek-quota.log"
FSTAB="$R/etc/fstab"
TEST_PROJECT=4190001
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l

MODE="${1:-}"; shift || true
ACK=""; EXPECT_IP=""; EXPECT_HOST=""; NO_REBOOT=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --expect-host) EXPECT_HOST="${2:-}"; shift 2 ;;
    --no-reboot) NO_REBOOT=1; shift ;;   # tests
    *) echo "REFUSE unknown argument: $1"; exit 2 ;;
  esac
done

now_wib() { TZ=Asia/Jakarta date '+%Y-%m-%d %H:%M:%S WIB'; }
FAILS=0
pass() { echo "PASS $*"; }
warn() { echo "WARN $*"; }
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
info() { echo "INFO $*"; }
refuse() { echo "REFUSE $*"; echo "RESULT $STEP $MODE REFUSE $*"; exit 3; }
die() { echo "FAIL $*"; echo "RESULT $STEP $MODE FAIL $*"; exit 1; }

case "$MODE" in PREFLIGHT|APPLY|VERIFY|REVERT) ;; *) refuse "mode must be PREFLIGHT, APPLY, VERIFY or REVERT" ;; esac
[ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
[ -n "$EXPECT_IP" ] && [ -n "$EXPECT_HOST" ] || refuse "--expect-ip and --expect-host are required"
[ "$(id -u)" = 0 ] || refuse "run with sudo"
if [ "$(hostname)" != "$EXPECT_HOST" ] || ! hostname -I 2>/dev/null | tr ' ' '\n' | grep -qxF "$EXPECT_IP"; then
  msg="❌ WRONG HOST: this is $(hostname) — run $STEP on $EXPECT_HOST ($EXPECT_IP); nothing done"
  echo "$msg"; echo "RESULT $STEP $MODE WRONG_HOST"; exit 4
fi

ROOT_DEV="$(findmnt -no SOURCE /)"
ROOT_FS="$(findmnt -no FSTYPE /)"
features() { tune2fs -l "$ROOT_DEV" 2>/dev/null | sed -n 's/^Filesystem features:[[:space:]]*//p'; }
has_feature() { features | tr ' ' '\n' | grep -qx "$1"; }
quota_on() { has_feature quota && has_feature project; }

# checks shared by PREFLIGHT and APPLY; prints PASS/FAIL lines
checks() {
  if [ "$ROOT_FS" = ext4 ]; then pass "root filesystem is ext4 ($ROOT_DEV)"; else fail "root filesystem is $ROOT_FS, not ext4"; fi
  local isz; isz="$(tune2fs -l "$ROOT_DEV" 2>/dev/null | sed -n 's/^Inode size:[[:space:]]*//p')"
  if [ "${isz:-0}" -ge 256 ] 2>/dev/null; then pass "inode size $isz (project quota needs 256)"; else fail "inode size ${isz:-unknown}: project quota needs 256"; fi
  for b in tune2fs e2fsck update-initramfs lsinitramfs; do
    if command -v "$b" >/dev/null; then pass "$b present"; else fail "$b missing"; fi
  done
  if compgen -G "$R/lib/modules/$(uname -r)/kernel/fs/quota/quota_v2.ko*" >/dev/null; then pass "kernel quota modules present"; else fail "kernel quota modules missing"; fi
  local free; free="$(df -Pm "$R/boot" 2>/dev/null | awk 'NR==2{print $4}')"
  if [ "${free:-0}" -ge 150 ] 2>/dev/null; then pass "/boot has ${free} MB free"; else fail "/boot has ${free:-?} MB free (need 150 for new initramfs files)"; fi
  local dr; dr="$(docker info -f '{{.DockerRootDir}}' 2>/dev/null || true)"
  if [ -n "$dr" ]; then
    if [ "$(findmnt -no SOURCE -T "$dr")" = "$ROOT_DEV" ]; then pass "Docker data ($dr) is on the root filesystem"; else warn "Docker data ($dr) is on another filesystem: its quota is not covered"; fi
  fi
}

hook_file() {
  cat <<'EOF'
#!/bin/sh
# Installed by Nostek Panel QUOTA-01 (one-shot; removed by VERIFY): tools for the step below.
PREREQ=""
prereqs() { echo "$PREREQ"; }
case "$1" in prereqs) prereqs; exit 0 ;; esac
. /usr/share/initramfs-tools/hook-functions
copy_exec /usr/sbin/tune2fs /usr/sbin
copy_exec /usr/sbin/e2fsck /usr/sbin
EOF
}

premount_file() {
  cat <<'EOF'
#!/bin/sh
# Installed by Nostek Panel QUOTA-01 (one-shot; removed by VERIFY). Before the root filesystem is mounted: check it,
# then turn on the ext4 features quota and project. Never stops the boot: every failure is only written to the log.
PREREQ=""
prereqs() { echo "$PREREQ"; }
case "$1" in prereqs) prereqs; exit 0 ;; esac
. /scripts/functions
LOG=/run/nostek-quota.log
main() {
  echo "start $(date)"
  DEV="$(resolve_device "$ROOT")"
  if [ -z "$DEV" ] || [ ! -b "$DEV" ]; then echo "skip: no root device ($ROOT)"; return 0; fi
  F="$(/usr/sbin/tune2fs -l "$DEV" 2>/dev/null | sed -n 's/^Filesystem features:[[:space:]]*//p')"
  case " $F " in *" project "*) case " $F " in *" quota "*) echo "done already"; return 0 ;; esac ;; esac
  /usr/sbin/e2fsck -fy "$DEV"; rc=$?
  if [ "$rc" -ge 4 ]; then echo "skip: e2fsck rc=$rc"; return 0; fi
  if /usr/sbin/tune2fs -O quota,project -Q usrquota,grpquota,prjquota "$DEV"; then echo "done"; else echo "fail: tune2fs rc=$?"; fi
}
main >>"$LOG" 2>&1
exit 0
EOF
}

initrd() { echo "$R/boot/initrd.img-$(uname -r)"; }

case "$MODE" in
PREFLIGHT)
  echo "INFO $STEP PREFLIGHT $(now_wib) (read-only)"
  if quota_on; then pass "quota and project are on already"; echo "RESULT $STEP PREFLIGHT PASS already_on"; exit 0; fi
  checks
  info "features now: $(features)"
  info "the reboot takes the server and every website down for about 3-10 minutes (filesystem check + quota scan)"
  info "keep the provider's web console (VNC) open as the way in if the boot needs help"
  if command -v systemctl >/dev/null; then systemctl list-timers --all 2>/dev/null | grep -iE 'restic|backup' | sed 's/^/INFO backup timer: /'; fi
  [ "$FAILS" = 0 ] || { echo "RESULT $STEP PREFLIGHT FAIL checks=$FAILS"; exit 1; }
  echo "RESULT $STEP PREFLIGHT PASS"
  ;;
APPLY)
  echo "INFO $STEP APPLY $(now_wib)"
  if quota_on; then echo "RESULT $STEP APPLY PASS already_on"; exit 0; fi
  checks
  [ "$FAILS" = 0 ] || die "checks failed ($FAILS): nothing changed"
  mkdir -p "$(dirname "$HOOK")" "$(dirname "$PREMOUNT")" "$STATE_DIR" "$BACKUP_DIR"
  cp -a "$FSTAB" "$BACKUP_DIR/fstab.$(date +%Y%m%d%H%M%S)"
  hook_file > "$HOOK"; premount_file > "$PREMOUNT"; chmod 0755 "$HOOK" "$PREMOUNT"
  if ! update-initramfs -u -k all >/dev/null 2>&1; then
    rm -f "$HOOK" "$PREMOUNT"; update-initramfs -u -k all >/dev/null 2>&1
    die "update-initramfs failed: one-shot step removed again, nothing else changed"
  fi
  l="$(lsinitramfs "$(initrd)" 2>/dev/null)"
  if ! grep -q 'scripts/local-premount/nostek-quota$' <<<"$l" || ! grep -q 'usr/sbin/tune2fs$' <<<"$l"; then
    rm -f "$HOOK" "$PREMOUNT"; update-initramfs -u -k all >/dev/null 2>&1
    die "the new initramfs lacks the one-shot step: removed again, nothing else changed"
  fi
  pass "one-shot step in $(initrd)"
  echo "applied=$(date +%s)" > "$STATE_DIR/state"
  echo "RESULT $STEP APPLY PASS reboot=now"
  echo "INFO the server reboots in 10 seconds; sign in again after 3-10 minutes and run VERIFY"
  if [ "$NO_REBOOT" = 0 ]; then sync; sleep 10; systemctl reboot; fi
  ;;
REVERT)
  rm -f "$HOOK" "$PREMOUNT"
  update-initramfs -u -k all >/dev/null 2>&1 || die "update-initramfs failed"
  echo "RESULT $STEP REVERT PASS"
  ;;
VERIFY)
  echo "INFO $STEP VERIFY $(now_wib)"
  [ -f "$LOG" ] && sed 's/^/INFO boot step: /' "$LOG" | tail -n 20
  if quota_on; then pass "ext4 features quota and project are on"; else
    die "quota/project are not on (boot step log above); the server runs as before. Run REVERT, then send the log"
  fi
  if [ -e "$HOOK" ] || [ -e "$PREMOUNT" ]; then
    rm -f "$HOOK" "$PREMOUNT"
    update-initramfs -u -k all >/dev/null 2>&1 || fail "update-initramfs after removing the one-shot step"
  fi
  if grep -q 'nostek-quota' <<<"$(lsinitramfs "$(initrd)" 2>/dev/null)"; then fail "one-shot step still in the initramfs"; else pass "one-shot step removed"; fi
  if ! command -v setquota >/dev/null || ! command -v quotaon >/dev/null; then
    apt-get -q -y -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 install quota >/dev/null 2>&1 || die "apt-get install quota failed"
  fi
  pass "quota tools present"
  # enforcement: now (quotaon), and at every boot (prjquota in fstab, safe now that the feature is on)
  if ! quotaon -Pp / 2>/dev/null | grep -q ' is on'; then quotaon -P / >/dev/null 2>&1 || true; fi
  if quotaon -Pp / 2>/dev/null | grep -q ' is on'; then pass "project quota enforced on /"; else fail "project quota not enforced on /"; fi
  if awk '$1 !~ /^#/ && $2 == "/" {print $4}' "$FSTAB" | grep -q prjquota; then pass "fstab has prjquota"; else
    cp -a "$FSTAB" "$BACKUP_DIR/fstab.verify.$(date +%Y%m%d%H%M%S)"
    awk 'BEGIN{OFS="\t"} $1 !~ /^#/ && $2 == "/" && $4 !~ /prjquota/ {$4=$4",prjquota"} {print}' "$FSTAB" > "$FSTAB.nostek" && mv "$FSTAB.nostek" "$FSTAB"
    if findmnt --verify --tab-file "$FSTAB" >/dev/null 2>&1; then pass "fstab: prjquota added for /"; else
      cp -a "$(ls -t "$BACKUP_DIR"/fstab.verify.* | head -n1)" "$FSTAB"; fail "fstab check failed: previous fstab put back"
    fi
  fi
  # proof: a test folder with a 1 MB limit refuses a 2 MB file
  T="$R/var/lib/nostek/quota-01/test"; rm -rf "$T"; mkdir -p "$T"
  if chattr +P -p "$TEST_PROJECT" "$T" && setquota -P "$TEST_PROJECT" 1024 1024 0 0 / \
     && ! dd if=/dev/zero of="$T/big" bs=1M count=2 status=none 2>/dev/null; then pass "a 1 MB project limit refuses a 2 MB file"; else
    fail "the test limit did not hold"
  fi
  rm -rf "$T"; setquota -P "$TEST_PROJECT" 0 0 0 0 / >/dev/null 2>&1 || true
  [ "$FAILS" = 0 ] || { echo "RESULT $STEP VERIFY FAIL checks=$FAILS"; exit 1; }
  echo "RESULT $STEP VERIFY PASS"
  ;;
esac
