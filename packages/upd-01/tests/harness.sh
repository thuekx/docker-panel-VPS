#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# UPD-01 harness: runs the updater against a fake root and a local test repository whose SITE-01 and PANEL-01
# scripts only record how they were called and keep a state file like the real ones.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../nostek-update.sh}")"
[ "$(id -u)" = 0 ] || { echo "run with sudo"; exit 2; }
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"; trap 'rm -rf "$TMP_ALL"' EXIT

REPO="$TMP_ALL/repo"; mkdir -p "$REPO/packages/panel-01" "$REPO/packages/site-01" "$REPO/packages/upd-01"
G() { git -C "$REPO" -c user.name=test -c user.email=test@example.test "$@"; }
fake_pkg() { # $1 package, $2 script
  cat > "$REPO/packages/$1/$2" <<'PKG'
#!/usr/bin/env bash
# fake package: records its arguments, keeps a state file, fails on request
name="$(basename "$0")"; mode="$1"; step="$(echo "$name" | cut -d- -f1-2 | tr a-z A-Z)"
pkg="$(echo "$step" | tr A-Z a-z)"; ver="v$(cat "$(dirname "$0")/../../VERSION")"
echo "$step $mode $ver $*" >> "$FAKE/pkg.log"
[ -f "$FAKE/break_${step}_${mode}_$ver" ] && { echo "FAIL fake"; echo "RESULT $step $mode FAIL fake"; exit 1; }
c=""; while [ $# -gt 0 ]; do [ "$1" = --src-commit ] && c="$2"; shift; done
st="$UPD01_TEST_ROOT/var/lib/nostek/$pkg/state"
if [ -n "$c" ]; then printf 'phase=APPLIED\ncommit=%s\nrelease=%s\n' "$c" "$ver" > "$st"; fi
echo "PROGRESS 1/1 fake"; echo "RESULT $step $mode PASS version=$ver"
PKG
  (cd "$REPO/packages/$1" && sha256sum "$2" > "$1.lock")
}
G init -q; G config uploadpack.allowAnySHA1InWant true
fake_pkg panel-01 panel-01-core.sh; fake_pkg site-01 site-01-agent.sh
cp "$SCRIPT" "$REPO/packages/upd-01/nostek-update.sh"; (cd "$REPO/packages/upd-01" && sha256sum nostek-update.sh > upd-01.lock)
echo 1.37.0 > "$REPO/VERSION"; G add -A; G commit -qm v1.37.0; G tag v1.37.0; C137="$(G rev-parse HEAD)"
echo 1.38.0 > "$REPO/VERSION"; G commit -qam v1.38.0; G tag v1.38.0; C138="$(G rev-parse HEAD)"
echo 1.39.0 > "$REPO/VERSION"; G commit -qam v1.39.0; C139="$(G rev-parse HEAD)"   # VERSION says 1.39.0
echo "# changed" >> "$REPO/packages/site-01/site-01-agent.sh"; echo 1.40.0 > "$REPO/VERSION"; G commit -qam tampered; CBAD="$(G rev-parse HEAD)"
G checkout -q v1.38.0

new_env() {
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export UPD01_TEST_ROOT="$T/root" UPD01_TEST_REPO="file://$REPO" FAKE="$T/fake" UPD01_TEST_DOCKER="$T/docker"
  local r="$UPD01_TEST_ROOT"
  mkdir -p "$FAKE" "$r/var/lib/nostek/site-01" "$r/var/lib/nostek/panel-01" "$r/srv/nostek/panel" "$r/srv/nostek/core"
  printf '#!/bin/sh\necho "docker $*" >> "%s/pkg.log"\n' "$FAKE" > "$UPD01_TEST_DOCKER"; chmod +x "$UPD01_TEST_DOCKER"
  printf 'phase=APPLIED\ncommit=%s\nrelease=v1.37.0\n' "$C137" > "$r/var/lib/nostek/site-01/state"
  cp "$r/var/lib/nostek/site-01/state" "$r/var/lib/nostek/panel-01/state"
  echo "new-db" > "$r/srv/nostek/panel/panel.db"; echo "old-db" > "$r/srv/nostek/panel/update.db"
}
A=(--ack UPD-01 --expect-ip 198.51.100.7 --expect-host fake-vps --domain panel.example.test --preview-zone preview.example.test)
run() { OUT="$(bash "$SCRIPT" "$@" 2>&1 </dev/null)"; RC=$?; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else echo "FAIL t$TESTS $1"; echo "$OUT" | tail -n 25 | sed 's/^/    | /'; BAD=$((BAD+1)); fi; }
has() { grep -q -- "$1" <<<"$OUT"; }
st() { grep "^$1=" "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/status" 2>/dev/null | tail -n1 | cut -d= -f2-; }
pk() { grep "^$2=" "$UPD01_TEST_ROOT/var/lib/nostek/$1/state" | cut -d= -f2-; }
log() { cat "$FAKE/pkg.log" 2>/dev/null; }
DB=(--db-copy "\$UPD01_TEST_ROOT/srv/nostek/panel/update.db")

# ---------- refusals ----------
new_env; run RUN --tag v1.38.0 --commit "$C138" --ack X; ok "no ack refused" '[ $RC = 3 ] && [ ! -f "$FAKE/pkg.log" ]'
new_env; run RUN "${A[@]}" --tag 1.38 --commit "$C138"; ok "bad tag refused" '[ $RC = 3 ] && has "must look like"'
new_env; run RUN "${A[@]}" --tag v1.38.0 --commit abc; ok "short commit refused" '[ $RC = 3 ]'
new_env; run RUN "${A[@]}" --tag v1.37.0 --commit "$C137"; ok "same release refused" '[ $RC = 3 ] && has "not newer"'
new_env; sed -i 's/^phase=.*/phase=APPLYING/' "$UPD01_TEST_ROOT/var/lib/nostek/panel-01/state"; run RUN "${A[@]}" --tag v1.38.0 --commit "$C138"
ok "unfinished panel refused" '[ $RC = 3 ] && has "both be installed"'
ok "a refusal is recorded for the panel" '[ "$(st phase)" = REFUSED ] && [ "$(st result)" = "agent and panel must both be installed (SITE-01 and PANEL-01 APPLIED)" ]'
new_env; mkdir -p "$UPD01_TEST_ROOT/var/lib/nostek/upd-01"; exec 8> "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/lock"; flock 8
run RUN "${A[@]}" --tag v1.38.0 --commit "$C138"; ok "second update refused while one runs" '[ $RC = 3 ] && has "another update" && [ ! -f "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/status" ]'; exec 8>&-

# ---------- release checks: nothing changes ----------
new_env; run RUN "${A[@]}" --tag v1.38.0 --commit "$C139"; ok "tag and VERSION must match" '[ $RC = 1 ] && has "holds version v1.39.0" && [ "$(st phase)" = FAILED ] && ! log | grep -q APPLY'
new_env; run RUN "${A[@]}" --tag v1.40.0 --commit "$CBAD"; ok "tampered script refused" '[ $RC = 1 ] && has "site-01 script does not match" && ! log | grep -q APPLY'
new_env; run RUN "${A[@]}" --tag v1.38.0 --commit 0123456789012345678901234567890123456789; ok "unknown commit fails" '[ $RC = 1 ] && has "cannot fetch" && [ "$(pk panel-01 commit)" = "$C137" ]'

# ---------- happy path ----------
new_env; eval "run RUN \"\${A[@]}\" --tag v1.38.0 --commit \"$C138\" ${DB[*]}"
ok "update passes" '[ $RC = 0 ] && [ "$(st phase)" = DONE ] && [ "$(st from)" = v1.37.0 ] && [ "$(st tag)" = v1.38.0 ]'
ok "order: agent APPLY, panel UPDATE, both VERIFY" '[ "$(log | awk "{print \$1, \$2, \$3}" | tr "\n" ,)" = "SITE-01 APPLY v1.38.0,PANEL-01 UPDATE v1.38.0,SITE-01 VERIFY v1.38.0,PANEL-01 VERIFY v1.38.0," ]'
ok "scripts get host, address, names and commit" 'log | grep -q "SITE-01 APPLY v1.38.0 APPLY --ack SITE-01 --expect-ip 198.51.100.7 --expect-host fake-vps --domain panel.example.test --preview-zone preview.example.test --src-commit $C138 --auto"'
ok "both on the new commit" '[ "$(pk site-01 commit)" = "$C138" ] && [ "$(pk panel-01 commit)" = "$C138" ]'
ok "database copy moved in and removed at the end" '[ ! -f "$UPD01_TEST_ROOT/srv/nostek/panel/update.db" ] && [ ! -f "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/panel.db.before" ]'
ok "progress for the panel" 'grep -q "^PROGRESS 2/4 agent v1.38.0" "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/progress" && grep -q "^  RESULT SITE-01 APPLY PASS" "$UPD01_TEST_ROOT/var/lib/nostek/upd-01/progress"'
ok "log is private" '[ "$(stat -c %a "$UPD01_TEST_ROOT/var/log/nostek-update.log")" = 600 ] && [ "$(stat -c %a "$UPD01_TEST_ROOT/var/lib/nostek/upd-01")" = 700 ]'
run STATUS; ok "status shows the result" '[ $RC = 0 ] && has "phase=DONE" && has "PROGRESS 4/4 verify"'

# ---------- failures and roll-back ----------
new_env; touch "$FAKE/break_SITE-01_APPLY_v1.38.0"; eval "run RUN \"\${A[@]}\" --tag v1.38.0 --commit \"$C138\" ${DB[*]}"
ok "agent failure stops before the panel" '[ $RC = 1 ] && [ "$(st phase)" = ROLLED_BACK ] && ! log | grep -q PANEL-01'
new_env; touch "$FAKE/break_PANEL-01_UPDATE_v1.38.0"; eval "run RUN \"\${A[@]}\" --tag v1.38.0 --commit \"$C138\" ${DB[*]}"
ok "panel failure puts the previous agent back" '[ $RC = 1 ] && [ "$(st phase)" = ROLLED_BACK ] && log | grep -q "SITE-01 APPLY v1.37.0" && [ "$(pk site-01 commit)" = "$C137" ]'
ok "panel failure restores the database" '[ "$(cat "$UPD01_TEST_ROOT/srv/nostek/panel/panel.db")" = old-db ] && log | grep -q "docker compose .* stop panel"'
new_env; touch "$FAKE/break_PANEL-01_VERIFY_v1.38.0"; eval "run RUN \"\${A[@]}\" --tag v1.38.0 --commit \"$C138\" ${DB[*]}"
ok "failed VERIFY rolls back both" '[ $RC = 1 ] && [ "$(st phase)" = ROLLED_BACK ] && [ "$(pk site-01 commit)" = "$C137" ] && [ "$(pk panel-01 commit)" = "$C137" ] && has "v1.37.0 is running again"'
ok "roll-back order: panel, database, agent" 'log | tail -n 4 | awk "{print \$1, \$2}" | tr "\n" , | grep -q "PANEL-01 UPDATE,docker compose,docker compose,SITE-01 APPLY,"'
ok "failed VERIFY restores the database" '[ "$(cat "$UPD01_TEST_ROOT/srv/nostek/panel/panel.db")" = old-db ]'
new_env; touch "$FAKE/break_SITE-01_VERIFY_v1.38.0" "$FAKE/break_SITE-01_APPLY_v1.37.0"; run RUN "${A[@]}" --tag v1.38.0 --commit "$C138"
ok "a failed roll-back is reported as FAILED" '[ $RC = 1 ] && [ "$(st phase)" = FAILED ] && has "putting v1.37.0 back also failed"'
ok "without a copy the database is kept" 'has "no database copy" && [ "$(cat "$UPD01_TEST_ROOT/srv/nostek/panel/panel.db")" = new-db ]'

echo "RESULT UPD-01 harness tests=$TESTS failed=$BAD"
[ "$BAD" = 0 ]
