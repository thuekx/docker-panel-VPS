#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# HOST-02 harness: runs the package against a fake root with stubbed system commands.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../host-02-baseline.sh}")"
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"; trap 'rm -rf "$TMP_ALL"' EXIT
new_env() {
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export HOST02_TEST_ROOT="$T/root" FAKE="$T/fake"
  local r="$HOST02_TEST_ROOT"
  mkdir -p "$FAKE" "$r"/etc/apt/apt.conf.d "$r"/etc/apt/sources.list.d "$r"/etc/fail2ban/jail.d "$r"/etc/systemd/system "$r"/proc "$r"/var/lib/nostek/host-01
  printf 'ID=ubuntu\nVERSION_ID="24.04"\nVERSION_CODENAME=noble\n' > "$r/etc/os-release"
  printf 'MemTotal:        4000000 kB\n' > "$r/proc/meminfo"
  echo "phase=DONE" > "$r/var/lib/nostek/host-01/state"
  touch "$r/etc/fstab"; echo 20022 > "$FAKE/listen"; : > "$FAKE/ufw_rules"
  export PATH="$HERE/stubs:/usr/sbin:/usr/bin:/sbin:/bin"
  export SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=10.0.0.5 FAKE_PUBLIC_IP=198.51.100.7
  unset FAKE_FPR || true
}
A=(--ack HOST-02 --ssh-port 20022 --expect-ip 198.51.100.7 --expect-host fake-vps)
S3=(--s3-endpoint https://s3.example.test --s3-bucket test-backup-bucket --s3-prefix vps1)
SECRETS=$'FAKEKEYID0001\nfake-secret-access-key-0001\nfake-backup-pass-0123456789'
run() { OUT="$(bash "$SCRIPT" "$@" 2>"$T/stderr" </dev/null)"; RC=$?; }
runin() { local in="$1"; shift; OUT="$(printf '%s\n' "$in" | bash "$SCRIPT" "$@" 2>"$T/stderr")"; RC=$?; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else echo "FAIL t$TESTS $1"; echo "$OUT" | tail -n 25 | sed 's/^/    | /'; BAD=$((BAD+1)); fi; }
has() { grep -q -- "$1" <<<"$OUT"; }
phase() { grep '^phase=' "$HOST02_TEST_ROOT/var/lib/nostek/host-02/state" 2>/dev/null | tail -n1 | cut -d= -f2; }

new_env; run PREFLIGHT "${A[@]}"; ok "preflight passes" '[ $RC = 0 ] && has "RESULT HOST-02 PREFLIGHT PASS"'
new_env; FAKE_HOSTNAME=other; run PREFLIGHT "${A[@]}"; ok "wrong host name stops" '[ $RC = 4 ] && grep -q "WRONG HOST" "$T/stderr"'
new_env; FAKE_PUBLIC_IP=203.0.113.1; run PREFLIGHT "${A[@]}"; ok "wrong address stops" '[ $RC = 4 ]'
new_env; unset SUDO_USER; run PREFLIGHT "${A[@]}"; ok "root shell refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT --ack HOST-02 --ssh-port 22 --expect-ip 198.51.100.7 --expect-host fake-vps; ok "wrong ssh port fails preflight" '[ $RC = 1 ] && has "not listening on 22"'
new_env; echo phase=APPLIED > "$HOST02_TEST_ROOT/var/lib/nostek/host-01/state"; run PREFLIGHT "${A[@]}"; ok "needs HOST-01 done" '[ $RC = 1 ]'
new_env; run APPLY "${A[@]}"; ok "apply passes" '[ $RC = 0 ] && has "RESULT HOST-02 APPLY PASS" && [ "$(phase)" = FIREWALL_PENDING ]'
ok "WIB timezone" '[ "$(cat "$FAKE/tz")" = Asia/Jakarta ]'
ok "revert time shown in WIB" 'has "revert_at=$(TZ=Asia/Jakarta date -d "+10 min" +%H:%M) WIB"'
ok "ssh rule before enable + revert timer" 'grep -qx 20022/tcp "$FAKE/ufw_rules" && [ -f "$FAKE/ufw_on" ] && grep -q "ufw --force disable" "$FAKE/timer" && [ "$(grep -n "systemd-run" "$FAKE/argv.log" | head -1 | cut -d: -f1)" -lt "$(grep -n "^ufw --force enable" "$FAKE/argv.log" | head -1 | cut -d: -f1)" ]'
ok "fail2ban jail uses ssh port" 'grep -qx "port = 20022" "$HOST02_TEST_ROOT/etc/fail2ban/jail.d/nostek.local"'
ok "docker repo signed-by + daemon log limits" 'grep -q "signed-by=/etc/apt/keyrings/docker.asc" "$HOST02_TEST_ROOT/etc/apt/sources.list.d/docker.list" && grep -q "\"max-size\": \"20m\"" "$HOST02_TEST_ROOT/etc/docker/daemon.json"'
ok "apt is non-interactive" 'grep -q "^apt-get" "$FAKE/argv.log" && [ -z "$(grep "^apt-get" "$FAKE/argv.log" | grep -v -- "Dpkg::Use-Pty=0")" ]'
ok "swap added" '[ -f "$FAKE/swap" ] && grep -q "^/swapfile " "$HOST02_TEST_ROOT/etc/fstab"'
run APPLY "${A[@]}"; ok "second apply refused" '[ $RC = 3 ]'
run CONFIRM "${A[@]}"; ok "confirm without new session refused" '[ $RC = 3 ] && [ -f "$FAKE/timer" ]'
echo 20022 > "$FAKE/established"; run CONFIRM "${A[@]}"; ok "confirm passes, timer cancelled" '[ $RC = 0 ] && [ ! -f "$FAKE/timer" ] && [ "$(phase)" = BASE_DONE ]'
runin "$SECRETS" BACKUP-SETUP "${A[@]}" --s3-endpoint http://s3.example.test --s3-bucket test-backup-bucket --s3-prefix vps1 --stdin; ok "plain http endpoint refused" '[ $RC = 3 ]'
runin $'FAKEKEYID0001\nfake-secret-access-key-0001\nshort' BACKUP-SETUP "${A[@]}" "${S3[@]}" --stdin; ok "short backup password refused" '[ $RC = 3 ] && [ ! -f "$HOST02_TEST_ROOT/etc/nostek/backup/restic.env" ]'
runin "$SECRETS" BACKUP-SETUP "${A[@]}" "${S3[@]}" --stdin; ok "backup setup passes" '[ $RC = 0 ] && [ "$(phase)" = BACKUP_READY ] && [ "$(cat "$FAKE/restic_repo_seen")" = "s3:https://s3.example.test/test-backup-bucket/vps1" ]'
ok "secrets never in argv or output; env is 600" '! grep -qE "fake-secret-access-key-0001|fake-backup-pass-0123456789|FAKEKEYID0001" "$FAKE/argv.log" && ! has fake-secret && [ "$(stat -c %a "$HOST02_TEST_ROOT/etc/nostek/backup/restic.env")" = 600 ] && [ "$(stat -c %a "$HOST02_TEST_ROOT/etc/nostek/backup")" = 700 ]'
ok "nightly timer 03:30 WIB enabled" 'grep -q "OnCalendar=\*-\*-\* 03:30:00 Asia/Jakarta" "$HOST02_TEST_ROOT/etc/systemd/system/nostek-backup.timer" && [ -f "$FAKE/backup_timer_on" ]'
run BACKUP-TEST "${A[@]}"; ok "backup test: restore proven, test items removed" '[ $RC = 0 ] && has "restore=ok" && has "sisa_uji=0" && [ "$(phase)" = DONE ] && [ ! -e "$HOST02_TEST_ROOT/var/backups/nostek/restore-test" ] && [ -z "$(find "$FAKE/repo/snap" -name "*-host02-test")" ]'
ok "nightly snapshot exists" 'has "nightly_snapshots=1"'
run VERIFY "${A[@]}"; ok "verify passes, hello-world removed" '[ $RC = 0 ] && has "sisa_uji=0" && [ ! -f "$FAKE/hello_image" ]'

new_env; export FAKE_FPR=0000000000000000000000000000000000000000; run APPLY "${A[@]}"; ok "wrong docker key fingerprint stops" '[ $RC = 1 ] && has "fingerprint mismatch" && [ ! -f "$HOST02_TEST_ROOT/etc/apt/sources.list.d/docker.list" ]'
new_env; touch "$FAKE/break_timer"; run APPLY "${A[@]}"; ok "no safety timer = firewall not enabled" '[ $RC = 1 ] && [ ! -f "$FAKE/ufw_on" ] && [ "$(phase)" = BASE_DONE ]'
new_env; touch "$FAKE/break_apt"; run APPLY "${A[@]}"; ok "apt failure stops before firewall" '[ $RC = 1 ] && [ ! -f "$FAKE/ufw_on" ]'
new_env; run APPLY "${A[@]}"; unset SUDO_USER; run REVERT "${A[@]}" --auto; ok "revert disables firewall" '[ $RC = 0 ] && [ ! -f "$FAKE/ufw_on" ] && [ "$(phase)" = REVERTED ]'
new_env; run APPLY "${A[@]}"; echo 20022 > "$FAKE/established"; run CONFIRM "${A[@]}"; touch "$FAKE/break_restore"; runin "$SECRETS" BACKUP-SETUP "${A[@]}" "${S3[@]}" --stdin; run BACKUP-TEST "${A[@]}"; ok "failed restore is FAIL, not DONE" '[ $RC = 1 ] && [ "$(phase)" = BACKUP_READY ]'
new_env; run APPLY "${A[@]}"; echo 20022 > "$FAKE/established"; run CONFIRM "${A[@]}"; touch "$FAKE/break_restic_init"; runin "$SECRETS" BACKUP-SETUP "${A[@]}" "${S3[@]}" --stdin; ok "bad S3 access is FAIL" '[ $RC = 1 ] && has "restic init failed"'

new_env; run APPLY "${A[@]}"; echo 20022 > "$FAKE/established"; run CONFIRM "${A[@]}"; runin "$SECRETS" BACKUP-SETUP "${A[@]}" "${S3[@]}" --stdin; touch "$FAKE/corrupt_restore"; run BACKUP-TEST "${A[@]}"; ok "corrupt restore is FAIL" '[ $RC = 1 ] && has "restored file differs"'

rm -rf "$TMP_ALL"/t.*; LEFT="$(find "$TMP_ALL" -mindepth 1 | wc -l)"
if [ "$BAD" -eq 0 ]; then echo "RESULT HOST-02 HARNESS PASS tests=$TESTS sisa_uji=$LEFT"; exit 0; fi
echo "RESULT HOST-02 HARNESS FAIL failed=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
