#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# HOST-01 harness: runs the package against a fake root with stubbed system commands.
# Usage: bash harness.sh [path/to/script]   -> prints RESULT HOST-01 HARNESS PASS|FAIL
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../host-01-user-ssh.sh}")"
TESTS=0; BAD=0
TMP_ALL="$(mktemp -d)"
cleanup() { rm -rf "$TMP_ALL"; }
trap cleanup EXIT

new_env() {
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export HOST01_TEST_ROOT="$T/root" FAKE="$T/fake" HOST01_SETTLE=0
  mkdir -p "$FAKE" "$HOST01_TEST_ROOT"/etc/ssh/sshd_config.d "$HOST01_TEST_ROOT"/etc/sudoers.d "$HOST01_TEST_ROOT"/home/ubuntu/.ssh /run/sshd
  printf 'ID=ubuntu\nVERSION_ID="24.04"\n' > "$HOST01_TEST_ROOT/etc/os-release"
  printf 'root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000::/home/ubuntu:/bin/bash\n' > "$HOST01_TEST_ROOT/etc/passwd"
  printf 'PasswordAuthentication no\n' > "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/50-cloud-init.conf"
  echo "ssh-ed25519 AAAAFAKEKEYFORTEST test" > "$HOST01_TEST_ROOT/home/ubuntu/.ssh/authorized_keys"
  echo "ubuntu ALL=(ALL) NOPASSWD:ALL" > "$HOST01_TEST_ROOT/etc/sudoers.d/90-cloud-init-users"
  ssh-keygen -q -t ed25519 -N '' -f "$T/hostkey" >/dev/null
  printf 'Include %s/etc/ssh/sshd_config.d/*.conf\nHostKey %s/hostkey\nUsePAM no\n' "$HOST01_TEST_ROOT" "$T" > "$HOST01_TEST_ROOT/etc/ssh/sshd_config"
  echo 22 > "$FAKE/listen"
  export PATH="$HERE/stubs:/usr/sbin:/usr/bin:/sbin:/bin"
  export SUDO_USER=ubuntu FAKE_LOCAL_IP=10.0.0.5 FAKE_PUBLIC_IP=198.51.100.7 FAKE_SSH_MODE=socket FAKE_UFW=inactive
  unset HOST01_EXPECT_FAIL || true
}
ARGS=(--ack HOST-01 --user testadmin --port 22022 --expect-ip 198.51.100.7)
PWD_OK="fake-Pass-0123456789"
run() { OUT="$(bash "$SCRIPT" "$@" 2>"$T/stderr")"; RC=$?; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else echo "FAIL t$TESTS $1"; echo "$OUT" | sed 's/^/    | /'; BAD=$((BAD+1)); fi; }
has() { grep -q -- "$1" <<<"$OUT"; }
phase() { grep '^phase=' "$HOST01_TEST_ROOT/var/lib/nostek/host-01/state" 2>/dev/null | tail -n1 | cut -d= -f2; }
listen() { sort -n "$FAKE/listen" | tr '\n' ' ' | sed 's/ $//'; }

# --- invocation guards
new_env; run PREFLIGHT "${ARGS[@]}"; ok "preflight passes" '[ $RC = 0 ] && has "RESULT HOST-01 PREFLIGHT PASS"'
new_env; run PREFLIGHT --user testadmin --port 22022 --expect-ip 198.51.100.7; ok "no --ack refused" '[ $RC = 3 ] && has REFUSE'
new_env; unset SUDO_USER; run PREFLIGHT "${ARGS[@]}"; ok "root shell refused" '[ $RC = 3 ] && has "no root shell"'
new_env; run PREFLIGHT --ack HOST-01 --user testadmin --port 22 --expect-ip 198.51.100.7; ok "port 22 refused" '[ $RC != 0 ]'
new_env; run PREFLIGHT --ack HOST-01 --user 'Bad;User' --port 22022 --expect-ip 198.51.100.7; ok "bad user name refused" '[ $RC = 3 ]'
new_env; FAKE_PUBLIC_IP=203.0.113.9; run PREFLIGHT "${ARGS[@]}"; ok "wrong host stops (stdout+stderr)" '[ $RC = 4 ] && has "WRONG HOST" && grep -q "WRONG HOST" "$T/stderr"'
new_env; FAKE_PUBLIC_IP=""; run PREFLIGHT "${ARGS[@]}"; ok "unknown identity refused" '[ $RC = 3 ]'
new_env; FAKE_PUBLIC_IP=""; FAKE_LOCAL_IP=198.51.100.7; run PREFLIGHT "${ARGS[@]}"; ok "local address match passes" '[ $RC = 0 ]'
new_env; printf 'ID=debian\nVERSION_ID="12"\n' > "$HOST01_TEST_ROOT/etc/os-release"; run PREFLIGHT "${ARGS[@]}"; ok "non-ubuntu fails" '[ $RC = 1 ]'
new_env; echo 22022 >> "$FAKE/listen"; run PREFLIGHT "${ARGS[@]}"; ok "port in use fails" '[ $RC = 1 ] && has "already in use"'

# --- APPLY guards
new_env; run APPLY "${ARGS[@]}" </dev/null; ok "apply without password refused, nothing changed" '[ $RC = 3 ] && [ ! -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" ]'
new_env; OUT="$(printf 'short\n' | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?; ok "short password refused" '[ $RC = 3 ] && [ ! -f "$FAKE/chpasswd.stdin" ]'
new_env; touch "$FAKE/break_restart"; OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?
ok "restart failure rolls back" '[ $RC = 1 ] && has rolled_back && [ ! -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" ] && [ -z "$(phase)" ]'
new_env; touch "$FAKE/break_listen"; OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?
ok "missing listener rolls back + timer stopped" '[ $RC = 1 ] && has "listen rolled_back" && [ ! -f "$FAKE/timer" ] && [ ! -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" ]'
new_env; touch "$FAKE/break_timer"; OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?
ok "no safety timer = rolled back, only 22" '[ $RC = 1 ] && has "timer rolled_back" && [ "$(listen)" = "22" ] && [ ! -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" ]'

# --- full flow
new_env; FAKE_UFW=active
OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?
ok "apply passes" '[ $RC = 0 ] && has "RESULT HOST-01 APPLY PASS"'
ok "both ports listen after apply" '[ "$(listen)" = "22 22022" ]'
ok "drop-in sorts before cloud-init and enables password login" 'grep -qx "Port 22" "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" && [ "$(/usr/sbin/sshd -T -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config" -C user=x,host=x,addr=127.0.0.1 2>/dev/null | grep -x "passwordauthentication yes")" ]'
ok "password only via stdin" '[ "$(cat "$FAKE/chpasswd.stdin")" = "testadmin:$PWD_OK" ] && ! grep -q "$PWD_OK" "$FAKE/argv.log" && ! has "$PWD_OK"'
ok "ufw rule added" 'grep -q "ufw allow 22022/tcp" "$FAKE/argv.log"'
ok "revert timer scheduled" '[ -f "$FAKE/timer" ] && grep -q "REVERT --ack HOST-01 --auto" "$FAKE/timer"'
ok "summary is last block" '[ "$(echo "$OUT" | tail -n1 | cut -d" " -f1-4)" = "RESULT HOST-01 APPLY PASS" ] && has "SUMMARY ====="'
run APPLY "${ARGS[@]}" </dev/null; ok "second apply refused" '[ $RC = 3 ]'
echo 22022 > "$FAKE/established"; run CONFIRM "${ARGS[@]}"; ok "confirm as old user refused" '[ $RC = 3 ] && [ -f "$FAKE/timer" ]'
rm -f "$FAKE/established"; export SUDO_USER=testadmin; run CONFIRM "${ARGS[@]}"; ok "confirm without session on new port refused" '[ $RC = 3 ]'
echo 22022 > "$FAKE/established"; run CONFIRM "${ARGS[@]}"; ok "confirm passes, timer cancelled" '[ $RC = 0 ] && [ ! -f "$FAKE/timer" ] && [ "$(phase)" = APPLIED ]'
SUDO_USER=ubuntu; run FINALIZE "${ARGS[@]}" --old-user ubuntu; ok "finalize as old user refused" '[ $RC = 3 ]'
export SUDO_USER=testadmin; run FINALIZE "${ARGS[@]}"; ok "finalize needs --old-user" '[ $RC = 3 ]'
run FINALIZE "${ARGS[@]}" --old-user ubuntu; ok "finalize passes" '[ $RC = 0 ] && [ "$(phase)" = FINALIZED_PENDING ]'
ok "only new port listens" '[ "$(listen)" = "22022" ]'
ok "old user locked, not deleted" 'grep -qx ubuntu "$FAKE/locked" && grep -q "^ubuntu:.*:/usr/sbin/nologin$" "$HOST01_TEST_ROOT/etc/passwd" && [ -f "$HOST01_TEST_ROOT/home/ubuntu/.ssh/authorized_keys.disabled-host01" ] && [ ! -f "$HOST01_TEST_ROOT/etc/sudoers.d/90-cloud-init-users" ]'
# simulate the timer firing (no confirm)
unset SUDO_USER; run REVERT "${ARGS[@]}" --auto; ok "auto-revert after finalize restores 22 + old user" '[ $RC = 0 ] && [ "$(listen)" = "22 22022" ] && [ ! -s "$FAKE/locked" ] && [ -f "$HOST01_TEST_ROOT/home/ubuntu/.ssh/authorized_keys" ] && [ -f "$HOST01_TEST_ROOT/etc/sudoers.d/90-cloud-init-users" ] && grep -q "^ubuntu:.*:/bin/bash$" "$HOST01_TEST_ROOT/etc/passwd" && [ "$(phase)" = APPLIED ]'
export SUDO_USER=testadmin; run FINALIZE "${ARGS[@]}" --old-user ubuntu; run CONFIRM "${ARGS[@]}"; ok "finalize + confirm = DONE" '[ $RC = 0 ] && [ "$(phase)" = DONE ] && [ "$(listen)" = "22022" ]'

# --- auto-revert after APPLY (no confirm)
new_env; OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"
unset SUDO_USER; run REVERT "${ARGS[@]}" --auto; ok "auto-revert after apply closes new port, files moved aside" '[ $RC = 0 ] && [ "$(listen)" = "22" ] && [ ! -f "$HOST01_TEST_ROOT/etc/ssh/sshd_config.d/05-nostek-host01.conf" ] && ls -d "$HOST01_TEST_ROOT"/var/backups/nostek/host-01/reverted-* >/dev/null 2>&1'
# --- 22.04 service mode
new_env; printf 'ID=ubuntu\nVERSION_ID="22.04"\n' > "$HOST01_TEST_ROOT/etc/os-release"; FAKE_SSH_MODE=service
OUT="$(printf '%s\n' "$PWD_OK" | bash "$SCRIPT" APPLY "${ARGS[@]}" --stdin 2>/dev/null)"; RC=$?; ok "22.04 service mode apply" '[ $RC = 0 ] && has "ssh_mode=service" && grep -q "systemctl restart ssh$" "$FAKE/argv.log"'

rm -rf "$TMP_ALL"/t.*; LEFT="$(find "$TMP_ALL" -mindepth 1 | wc -l)"
if [ "$BAD" -eq 0 ]; then echo "RESULT HOST-01 HARNESS PASS tests=$TESTS sisa_uji=$LEFT"; exit 0; fi
echo "RESULT HOST-01 HARNESS FAIL failed=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
