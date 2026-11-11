#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# PANEL-01 harness: runs the package against a fake root with stubbed host identity and DNS, and the REAL Docker
# engine (builds the image, starts the core stack on ports 80/443, tests it, removes it again).
# Needs: root, Docker with compose, git, ports 80/443 free. The source is the commit checked out in this repository.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../panel-01-core.sh}")"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"
cleanup() {
  docker rm -f np-test-pub >/dev/null 2>&1
  docker ps -aq --filter label=com.docker.compose.project=nostek-core | xargs -r docker rm -f >/dev/null 2>&1
  docker network rm nostek-edge nostek-ctl >/dev/null 2>&1
  rm -rf "$TMP_ALL"
}
trap cleanup EXIT
new_env() {
  cleanup; TMP_ALL="$(mktemp -d)"
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export PANEL01_TEST_ROOT="$T/root" PANEL01_TEST_REPO="file://$REPO" PANEL01_TEST_ACME_CA="https://127.0.0.1:1/directory" PANEL01_CERT_WAIT=5
  mkdir -p "$PANEL01_TEST_ROOT/var/lib/nostek/host-02"
  echo "phase=DONE" > "$PANEL01_TEST_ROOT/var/lib/nostek/host-02/state"
  export PATH="$HERE/stubs:/usr/sbin:/usr/bin:/sbin:/bin"
  export SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=198.51.100.7 FAKE_DNS=198.51.100.7; unset FAKE_PUBLIC_IP || true
}
D=panel.example.test
A=(--ack PANEL-01 --expect-ip 198.51.100.7 --expect-host fake-vps --domain "$D")
run() { OUT="$(bash "$SCRIPT" "$@" 2>"$T/stderr" </dev/null)"; RC=$?; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else echo "FAIL t$TESTS $1"; echo "$OUT" | tail -n 25 | sed 's/^/    | /'; BAD=$((BAD+1)); fi; }
has() { grep -q -- "$1" <<<"$OUT"; }
phase() { grep '^phase=' "$PANEL01_TEST_ROOT/var/lib/nostek/panel-01/state" 2>/dev/null | tail -n1 | cut -d= -f2; }
CORE() { echo "$PANEL01_TEST_ROOT/srv/nostek/core"; }

# --- refusals and preflight (nothing changes)
new_env; FAKE_HOSTNAME=other; run PREFLIGHT "${A[@]}"; ok "wrong host name stops" '[ $RC = 4 ] && grep -q "WRONG HOST" "$T/stderr"'
new_env; export FAKE_LOCAL_IP=10.0.0.5 FAKE_PUBLIC_IP=203.0.113.1; run PREFLIGHT "${A[@]}"; ok "wrong address stops" '[ $RC = 4 ]'
new_env; unset SUDO_USER; run PREFLIGHT "${A[@]}"; ok "root shell refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT --ack PANEL-01 --expect-ip 198.51.100.7 --expect-host fake-vps --domain 'Panel.Example.test;x'; ok "bad domain refused" '[ $RC = 3 ]'
new_env; run APPLY "${A[@]}" --src-commit main; ok "commit must be a full id" '[ $RC = 3 ] && [ ! -d "$(CORE)" ]'
new_env; echo phase=BASE_DONE > "$PANEL01_TEST_ROOT/var/lib/nostek/host-02/state"; run PREFLIGHT "${A[@]}"; ok "needs HOST-02 done" '[ $RC = 1 ] && has "HOST-02 is not DONE"'
new_env; FAKE_DNS=203.0.113.9; run PREFLIGHT "${A[@]}"; ok "dns must point to this host" '[ $RC = 1 ] && has "does not point"'
new_env; run PREFLIGHT "${A[@]}"; ok "preflight passes and changes nothing" '[ $RC = 0 ] && has "RESULT PANEL-01 PREFLIGHT PASS" && [ ! -e "$PANEL01_TEST_ROOT/srv" ]'
new_env; run APPLY "${A[@]}" --src-commit 0000000000000000000000000000000000000000; ok "unknown commit stops before any change" '[ $RC = 1 ] && [ ! -d "$(CORE)" ] && [ -z "$(phase)" ]'

# --- full life cycle on the real engine
new_env; run APPLY "${A[@]}" --src-commit "$COMMIT"; ok "apply passes" '[ $RC = 0 ] && has "RESULT PANEL-01 APPLY PASS" && [ "$(phase)" = APPLIED ]'
ok ".env is 600, data dirs owned by panel user" '[ "$(stat -c %a "$(CORE)/.env")" = 600 ] && [ "$(stat -c %u "$PANEL01_TEST_ROOT/srv/nostek/panel")" = 65532 ] && [ "$(stat -c %a "$PANEL01_TEST_ROOT/srv/nostek/edge/acme")" = 700 ]'
ok "source fetched at the exact commit" '[ "$(git -C "$PANEL01_TEST_ROOT/srv/nostek/src/$COMMIT" rev-parse HEAD)" = "$COMMIT" ]'
ok "only edge publishes, on 80 and 443" '[ "$(docker ps --filter label=com.docker.compose.project=nostek-core --format "{{.Ports}}" | grep -c "0.0.0.0:")" = 1 ] && docker ps --filter label=com.docker.compose.service=edge --format "{{.Ports}}" | grep -q "0.0.0.0:443->8443"'
ok "http redirects, https serves the login" '[ "$(curl -s -o /dev/null -w "%{http_code}" -H "Host: '"$D"'" http://127.0.0.1/)" = 301 ] && curl -sk --resolve '"$D"':443:127.0.0.1 https://'"$D"'/login | grep -q "np_csrf\|name=\"password\""'
run APPLY "${A[@]}" --src-commit "$COMMIT"; ok "second apply refused" '[ $RC = 3 ]'
run SETUP-LINK "${A[@]}"; L1="$(grep -oE "https://$D/setup/[A-Za-z0-9_-]+" <<<"$OUT")"; ok "setup link printed" '[ $RC = 0 ] && [ -n "$L1" ] && has "valid_until="'
run SETUP-LINK "${A[@]}"; ok "new setup link cancels the old one" '[ $RC = 0 ] && [ "$(curl -sk -o /dev/null -w "%{http_code}" --resolve '"$D"':443:127.0.0.1 "$L1")" != 200 ]'
run ADMIN-ROTATE "${A[@]}" --email nobody@example.test; ok "rotate for unknown admin refused" '[ $RC = 3 ]'
run VERIFY "${A[@]}"; ok "verify passes" '[ $RC = 0 ] && has "RESULT PANEL-01 VERIFY PASS" && has "sisa_uji=0" && has "socket proxy refuses writes"'
docker run -d --name np-test-pub -p 127.0.0.1:18080:8080 "nostek/panel:${COMMIT:0:12}" >/dev/null 2>&1
run VERIFY "${A[@]}"; ok "verify fails when another container publishes ports" '[ $RC = 1 ] && has "other than edge publish"'
docker rm -f np-test-pub >/dev/null 2>&1
echo keep > "$PANEL01_TEST_ROOT/srv/nostek/panel/marker"
run REVERT "${A[@]}"; ok "revert stops stack, moves core aside, keeps data" '[ $RC = 0 ] && [ "$(phase)" = REVERTED ] && [ ! -d "$(CORE)" ] && ls -d "$PANEL01_TEST_ROOT"/var/backups/nostek/panel-01/*-revert/core >/dev/null 2>&1 && [ -f "$PANEL01_TEST_ROOT/srv/nostek/panel/marker" ] && [ -z "$(docker ps -q --filter label=com.docker.compose.project=nostek-core)" ]'
run APPLY "${A[@]}" --src-commit "$COMMIT"; ok "apply again after revert reuses source and image" '[ $RC = 0 ] && has "already present" && has "already built"'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$( { docker ps -aq --filter label=com.docker.compose.project=nostek-core; docker ps -aq --filter name=np-test-pub; } | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT PANEL-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT PANEL-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
