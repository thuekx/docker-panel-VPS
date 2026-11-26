#!/usr/bin/env bash
# Nostek Panel - UPD-01: update the agent and the panel to a release, started from the panel (Settings -> Update).
# The agent starts it as its own systemd unit, so restarting the agent or the panel does not stop it.
# Modes: RUN | STATUS (read-only)
# Usage: bash nostek-update.sh RUN --ack UPD-01 --tag vX.Y.Z --commit SHA --expect-ip ADDR --expect-host NAME
#        --domain PANEL_DOMAIN --preview-zone ZONE [--db-copy FILE]
# Steps: fetch the release by commit (git checks the content) and check its scripts against their sha256 lock
# files; SITE-01 APPLY; PANEL-01 UPDATE; SITE-01 VERIFY and PANEL-01 VERIFY. Any failure puts the previous release
# back (both scripts roll themselves back on their own failures; a failed VERIFY rolls back both and the panel's
# database copy). Websites keep running throughout.
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="UPD-01"
R="${UPD01_TEST_ROOT:-}"
NOSTEK="$R/srv/nostek"
SRC_ROOT="$NOSTEK/src"
CORE="$NOSTEK/core"
PANEL_DB="$NOSTEK/panel/panel.db"
VARLIB="$R/var/lib/nostek"
STATE_DIR="$VARLIB/upd-01"
LOG="$R/var/log/nostek-update.log"
REPO_URL="https://github.com/thuekx/docker-panel-VPS.git"
DOCKER=docker
if [ -n "$R" ]; then REPO_URL="${UPD01_TEST_REPO:-$REPO_URL}"; DOCKER="${UPD01_TEST_DOCKER:-true}"; fi
PANEL_UID=65532

MODE="${1:-}"; shift || true
ACK=""; TAG=""; COMMIT=""; IP=""; HOST=""; DOMAIN=""; ZONE=""; DBCOPY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --commit) COMMIT="${2:-}"; shift 2 ;;
    --expect-ip) IP="${2:-}"; shift 2 ;;
    --expect-host) HOST="${2:-}"; shift 2 ;;
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --preview-zone) ZONE="${2:-}"; shift 2 ;;
    --db-copy) DBCOPY="${2:-}"; shift 2 ;;
    *) echo "REFUSE unknown argument: $1"; exit 2 ;;
  esac
done

now() { date +%s; }
# every line goes to the full log; the lines the panel shows also go to progress (no secrets are ever printed)
say() { echo "$*"; echo "$*" >> "$STATE_DIR/progress"; }
info() { say "INFO $*"; }
pass() { say "PASS $*"; }
warn() { say "WARN $*"; }
progress() { state_set step "$1"; say "PROGRESS $1 $2"; }
state_get() { [ -f "$STATE_DIR/status" ] && grep -E "^$1=" "$STATE_DIR/status" | tail -n1 | cut -d= -f2-; }
state_set() {
  mkdir -p "$STATE_DIR"; touch "$STATE_DIR/status"
  grep -vE "^$1=" "$STATE_DIR/status" > "$STATE_DIR/status.tmp"; echo "$1=$2" >> "$STATE_DIR/status.tmp"
  mv "$STATE_DIR/status.tmp" "$STATE_DIR/status"
}
finish() { # $1 phase DONE|FAILED|ROLLED_BACK, $2 text
  state_set phase "$1"; state_set ended "$(now)"; state_set result "$2"
  say "RESULT $STEP RUN $1 $2"; rm -f "$STATE_DIR/panel.db.before"
  [ "$1" = DONE ] && exit 0; exit 1
}
refuse() { echo "REFUSE $*"; echo "RESULT $STEP $MODE REFUSE $*"; exit 3; }
ver_gt() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n1)" = "$1" ]; } # $1 > $2
pkg_get() { grep -E "^$2=" "$VARLIB/$1/state" 2>/dev/null | tail -n1 | cut -d= -f2-; }

# run_pkg TREE PACKAGE SCRIPT ARGS...: runs a package script of a release tree; its RESULT line decides.
run_pkg() {
  local tree="$1" pkg="$2" script="$3" out rc; shift 3
  out="$(bash "$tree/packages/$pkg/$script" "$@" --auto < /dev/null 2>&1)"; rc=$?
  printf '%s\n' "$out" >> "$LOG"
  printf '%s\n' "$out" | grep -E '^(PROGRESS|PASS|WARN|FAIL|RESULT) ' | sed "s/^/  /" >> "$STATE_DIR/progress"
  [ "$rc" = 0 ] && printf '%s\n' "$out" | grep -qE "^RESULT [A-Z0-9-]+ [A-Z-]+ PASS"
}
site01() { run_pkg "$1" site-01 site-01-agent.sh "$2" --ack SITE-01 --expect-ip "$IP" --expect-host "$HOST" --domain "$DOMAIN" --preview-zone "$ZONE" "${@:3}"; }
panel01() { run_pkg "$1" panel-01 panel-01-core.sh "$2" --ack PANEL-01 --expect-ip "$IP" --expect-host "$HOST" --domain "$DOMAIN" "${@:3}"; }

fetch_release() { # $1 commit; sets TREE
  local c="$1" dst="$SRC_ROOT/$1" tmp
  mkdir -p "$SRC_ROOT"
  if ! { [ -d "$dst/.git" ] && [ "$(git -C "$dst" rev-parse HEAD 2>/dev/null)" = "$c" ] && [ -z "$(git -C "$dst" status --porcelain 2>/dev/null)" ]; }; then
    [ -e "$dst" ] && rm -rf "$dst"
    tmp="$(mktemp -d "$SRC_ROOT/.fetch.XXXX")"
    git -C "$tmp" init -q && git -C "$tmp" fetch -q --depth 1 "$REPO_URL" "$c" >> "$LOG" 2>&1 && git -C "$tmp" -c advice.detachedHead=false checkout -q FETCH_HEAD || { rm -rf "$tmp"; say "FAIL cannot fetch $c"; return 1; }
    [ "$(git -C "$tmp" rev-parse HEAD)" = "$c" ] || { rm -rf "$tmp"; say "FAIL fetched commit does not match"; return 1; }
    mv "$tmp" "$dst"
  fi
  TREE="$dst"
}

check_release() { # the tag's VERSION and the scripts' sha256
  local v pkg; v="$(tr -d '[:space:]' < "$TREE/VERSION" 2>/dev/null)"
  [ "v$v" = "$TAG" ] || { say "FAIL tag $TAG holds version v${v:-none}"; return 1; }
  for pkg in site-01 panel-01 upd-01; do
    [ -f "$TREE/packages/$pkg/$pkg.lock" ] || { say "FAIL $pkg has no lock file"; return 1; }
    (cd "$TREE/packages/$pkg" && sha256sum --quiet -c "$pkg.lock" >/dev/null 2>&1) || { say "FAIL $pkg script does not match its sha256"; return 1; }
  done
  pass "release $TAG = commit $COMMIT; its scripts match their sha256"
}

restore_db() {
  [ -f "$STATE_DIR/panel.db.before" ] || { warn "no database copy: the panel keeps its current database"; return 0; }
  $DOCKER compose --project-directory "$CORE" -f "$CORE/compose.yml" --env-file "$CORE/.env" stop panel >> "$LOG" 2>&1
  rm -f "$PANEL_DB-wal" "$PANEL_DB-shm"
  cp "$STATE_DIR/panel.db.before" "$PANEL_DB.restore" && chown "$PANEL_UID:$PANEL_UID" "$PANEL_DB.restore" 2>/dev/null
  chmod 600 "$PANEL_DB.restore"; mv "$PANEL_DB.restore" "$PANEL_DB"
  $DOCKER compose --project-directory "$CORE" -f "$CORE/compose.yml" --env-file "$CORE/.env" start panel >> "$LOG" 2>&1
  pass "panel database from before the update put back"
}

# rollback_all puts the previous agent and panel back after the new ones were both installed.
rollback_all() {
  progress R1/3 "panel back to $OLD_REL"
  local ok=1 oldtree
  if fetch_release "$OLD_PANEL"; then oldtree="$TREE"; panel01 "$oldtree" UPDATE --src-commit "$OLD_PANEL" || { ok=0; say "FAIL previous panel did not start"; }
  else ok=0; fi
  restore_db
  progress R2/3 "agent back to $OLD_REL"
  if fetch_release "$OLD_SITE"; then site01 "$TREE" APPLY --src-commit "$OLD_SITE" || { ok=0; say "FAIL previous agent did not start"; }
  else ok=0; fi
  progress R3/3 "check"
  [ "$ok" = 1 ] && finish ROLLED_BACK "$1; $OLD_REL is running again"
  finish FAILED "$1; putting $OLD_REL back also failed: see $LOG"
}

run() {
  [ "$ACK" = UPD-01 ] || refuse "--ack UPD-01 is required"
  [ "$(id -u)" = 0 ] || refuse "must run as root"
  [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || refuse "--tag must look like v1.2.3"
  [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "--commit must be a full 40-character commit id"
  [ -n "$IP" ] && [ -n "$HOST" ] && [ -n "$DOMAIN" ] && [ -n "$ZONE" ] || refuse "--expect-ip, --expect-host, --domain and --preview-zone are required"
  mkdir -p "$STATE_DIR" "$(dirname "$LOG")"; chmod 700 "$STATE_DIR"; touch "$LOG"; chmod 600 "$LOG"
  exec 9> "$STATE_DIR/lock"
  flock -n 9 || refuse "another update is running"
  [ "$(pkg_get site-01 phase)" = APPLIED ] && [ "$(pkg_get panel-01 phase)" = APPLIED ] || refuse "agent and panel must both be installed (SITE-01 and PANEL-01 APPLIED)"
  OLD_SITE="$(pkg_get site-01 commit)"; OLD_PANEL="$(pkg_get panel-01 commit)"; OLD_REL="$(pkg_get panel-01 release)"
  [[ "$OLD_SITE" =~ ^[0-9a-f]{40}$ ]] && [[ "$OLD_PANEL" =~ ^[0-9a-f]{40}$ ]] || refuse "the running commits are unknown"
  ver_gt "${TAG#v}" "${OLD_REL#v}" || refuse "$TAG is not newer than the running $OLD_REL"

  : > "$STATE_DIR/progress"; rm -f "$STATE_DIR/status"
  state_set phase RUNNING; state_set tag "$TAG"; state_set commit "$COMMIT"; state_set from "$OLD_REL"; state_set started "$(now)"
  echo "===== $STEP $TAG from $OLD_REL $(date -u '+%Y-%m-%d %H:%M:%S UTC') =====" >> "$LOG"
  if [ -n "$DBCOPY" ]; then
    if [ -f "$DBCOPY" ] && [ ! -L "$DBCOPY" ]; then
      ( umask 077; cp "$DBCOPY" "$STATE_DIR/panel.db.before" ) && rm -f "$DBCOPY" && pass "panel database copy kept for a roll-back"
    else warn "the panel's database copy is missing: a roll-back keeps the current database"; fi
  fi

  progress 1/4 "fetch $TAG"
  fetch_release "$COMMIT" || finish FAILED "cannot fetch $TAG; nothing changed"
  check_release || finish FAILED "release check failed; nothing changed"
  NEW="$TREE"

  progress 2/4 "agent $TAG"
  site01 "$NEW" APPLY --src-commit "$COMMIT" || finish ROLLED_BACK "agent update failed; SITE-01 put $OLD_REL back"

  progress 3/4 "panel $TAG"
  if ! panel01 "$NEW" UPDATE --src-commit "$COMMIT"; then
    say "WARN panel update failed; PANEL-01 put the previous panel back"
    restore_db
    if fetch_release "$OLD_SITE" && site01 "$TREE" APPLY --src-commit "$OLD_SITE"; then finish ROLLED_BACK "panel update failed; $OLD_REL is running again"; fi
    finish FAILED "panel update failed and the previous agent did not start: see $LOG"
  fi

  progress 4/4 "verify"
  site01 "$NEW" VERIFY || rollback_all "agent VERIFY failed"
  panel01 "$NEW" VERIFY || rollback_all "panel VERIFY failed"
  finish DONE "$TAG is running"
}

status() {
  [ -f "$STATE_DIR/status" ] || { echo "RESULT $STEP STATUS NONE"; exit 0; }
  cat "$STATE_DIR/status"; echo "----"; tail -n 60 "$STATE_DIR/progress" 2>/dev/null
}

case "$MODE" in
  RUN) run ;;
  STATUS) status ;;
  *) refuse "mode must be RUN or STATUS" ;;
esac
