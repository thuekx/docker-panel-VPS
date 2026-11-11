#!/usr/bin/env bash
# Nostek Panel - PANEL-01: core stack (edge HTTPS proxy, read-only Docker socket proxy, Nostek Panel).
# Modes: PREFLIGHT (read-only) | APPLY | SETUP-LINK | ADMIN-ROTATE | VERIFY (read-only) | REVERT
# Usage: sudo bash panel-01-core.sh MODE --ack PANEL-01 --expect-ip ADDR --expect-host NAME --domain PANEL_DOMAIN
#        APPLY also: --src-commit SHA (40 hex; the repository commit to build from)
#        ADMIN-ROTATE also: --email ADMIN_EMAIL
# Source is fetched by commit id and checked with git (the id is a hash of the content), images are pinned by digest.
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="PANEL-01"; VERSION="1"
R="${PANEL01_TEST_ROOT:-}"
NOSTEK="$R/srv/nostek"
CORE="$NOSTEK/core"
SRC_ROOT="$NOSTEK/src"
VARLIB="$R/var/lib/nostek"
STATE_DIR="$VARLIB/panel-01"
BACKUP_ROOT="$R/var/backups/nostek/panel-01"
HOST02_STATE="$VARLIB/host-02/state"
REPO_URL="https://github.com/thuekx/docker-panel-VPS.git"
ACME_CA=""; TLS_TEST=0
if [ -n "$R" ]; then   # test hooks: only active with a test root
  REPO_URL="${PANEL01_TEST_REPO:-$REPO_URL}"; ACME_CA="${PANEL01_TEST_ACME_CA:-}"; TLS_TEST=1
  [ "${PANEL01_TEST_STRICT_TLS:-0}" = 1 ] && TLS_TEST=0   # exercise the production certificate path
fi
PANEL_UID=65532
CERT_WAIT="${PANEL01_CERT_WAIT:-150}"

MODE="${1:-}"; shift || true
ACK=""; EXPECT_IP=""; EXPECT_HOST=""; DOMAIN=""; COMMIT=""; EMAIL=""; AUTO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --expect-host) EXPECT_HOST="${2:-}"; shift 2 ;;
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --src-commit) COMMIT="${2:-}"; shift 2 ;;
    --email) EMAIL="${2:-}"; shift 2 ;;
    --auto) AUTO=1; shift ;;
    *) echo "REFUSE unknown argument: $1"; exit 2 ;;
  esac
done

now_wib() { TZ=Asia/Jakarta date '+%Y-%m-%d %H:%M:%S WIB'; }
FAILS=0
pass() { echo "PASS $*"; }
warn() { echo "WARN $*"; }
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
info() { echo "INFO $*"; }
progress() { echo "PROGRESS $1 $2"; }
refuse() { echo "REFUSE $*"; echo "RESULT $STEP $MODE REFUSE $*"; exit 3; }
die() { echo "FAIL $*"; summary FAIL "$*"; echo "RESULT $STEP $MODE FAIL $*"; exit 1; }
summary() {
  local s="$1"; shift
  echo "SUMMARY ===== $STEP $MODE: $s ($(now_wib)) ====="
  for l in "$@"; do echo "SUMMARY $l"; done
  echo "SUMMARY ======================================================="
}
wrong_host() {
  local h msg; h="$(hostname 2>/dev/null || echo unknown)"
  msg="❌ WRONG HOST: this is $h — run $STEP on $EXPECT_HOST ($EXPECT_IP); nothing done"
  echo "$msg"; echo "$msg" >&2; { [ -w /dev/tty ] && echo "$msg" > /dev/tty; } 2>/dev/null || true
  echo "RESULT $STEP $MODE WRONG_HOST"; exit 4
}
state_get() { [ -f "$STATE_DIR/state" ] && grep -E "^$1=" "$STATE_DIR/state" | tail -n1 | cut -d= -f2-; }
state_set() {
  mkdir -p "$STATE_DIR"; touch "$STATE_DIR/state"
  grep -vE "^$1=" "$STATE_DIR/state" > "$STATE_DIR/state.tmp" || true
  echo "$1=$2" >> "$STATE_DIR/state.tmp"; mv "$STATE_DIR/state.tmp" "$STATE_DIR/state"
}

check_invocation() {
  case "$MODE" in PREFLIGHT|APPLY|SETUP-LINK|ADMIN-ROTATE|VERIFY|REVERT) ;; *) refuse "mode must be PREFLIGHT|APPLY|SETUP-LINK|ADMIN-ROTATE|VERIFY|REVERT" ;; esac
  [ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
  [ "$(id -u)" = "0" ] || refuse "run with sudo"
  if [ "$AUTO" = "0" ] && [ -z "${SUDO_USER:-}" ]; then refuse "no root shell: run from your normal user with sudo"; fi
  [ -n "$EXPECT_IP" ] && [ -n "$EXPECT_HOST" ] || refuse "--expect-ip and --expect-host are required"
  [[ "$DOMAIN" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$ ]] || refuse "--domain must be a lower-case host name such as panel.example.com"
  if [ "$MODE" = APPLY ]; then [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "--src-commit must be a full 40-character commit id"; fi
  if [ "$MODE" = ADMIN-ROTATE ]; then [[ "$EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+$ ]] || refuse "--email must be the admin e-mail"; fi
}
check_host() {
  local h ips pub url
  h="$(hostname 2>/dev/null)"; [ "$h" = "$EXPECT_HOST" ] || wrong_host
  ips="$(ip -o -4 addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1)"
  if printf '%s\n' "$ips" | grep -qxF "$EXPECT_IP"; then pass "host identity $h / $EXPECT_IP (local)"; return 0; fi
  for url in https://api.ipify.org https://ifconfig.me/ip; do
    pub="$(curl -fsS --max-time 6 "$url" 2>/dev/null | tr -d '[:space:]')"
    if [ -n "$pub" ]; then [ "$pub" = "$EXPECT_IP" ] || wrong_host; pass "host identity $h / $EXPECT_IP (public)"; return 0; fi
  done
  refuse "cannot prove host identity (public address lookup failed)"
}

dc() { docker compose --project-directory "$CORE" -f "$CORE/compose.yml" --env-file "$CORE/.env" "$@"; }
host02_phase() { grep -E '^phase=' "$HOST02_STATE" 2>/dev/null | tail -n1 | cut -d= -f2; }
# services of our project that publish ports, other than edge
foreign_publishers() {
  local id name ports
  for id in $(docker ps -q 2>/dev/null); do
    name="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.project"}}|{{.Name}}' "$id" 2>/dev/null)"
    ports="$(docker port "$id" 2>/dev/null)"
    [ -z "$ports" ] && continue
    [ "$name" = "edge|nostek-core|${name##*|}" ] && continue
    echo "${name##*|}"
  done
}
svc_state() { # prints "running healthy" etc for a compose service
  local id; id="$(dc ps -q "$1" 2>/dev/null | head -n1)"
  [ -n "$id" ] || { echo "absent"; return; }
  docker inspect -f '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null
}
https_check() { # $1 path; prints http code. Reachability only: the certificate may still be pending (cert_ok checks it)
  curl -sS -k --noproxy '*' --max-time 8 -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN$1" 2>/dev/null
}
cert_ok() {
  [ "$TLS_TEST" = 1 ] && return 0
  curl -fsS --noproxy '*' --max-time 8 -o /dev/null --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/healthz" 2>/dev/null
}

# ---------- PREFLIGHT ----------
preflight() {
  progress 1/7 "host identity"; check_host
  progress 2/7 "HOST-02 state"
  if [ "$(host02_phase)" = DONE ]; then pass "HOST-02 done (baseline, firewall, backup proven)"; else fail "HOST-02 is not DONE (phase=$(host02_phase)); finish HOST-02 first"; fi
  progress 3/7 "docker"
  if docker info >/dev/null 2>&1; then pass "docker engine $(docker version --format '{{.Server.Version}}' 2>/dev/null)"; else fail "docker engine not running"; fi
  if docker compose version >/dev/null 2>&1; then pass "docker compose $(docker compose version --short 2>/dev/null)"; else fail "docker compose plugin missing"; fi
  command -v git >/dev/null 2>&1 && pass "git present" || fail "git missing (apt-get install git)"
  progress 4/7 "DNS $DOMAIN"
  local a; a="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')"
  if printf '%s\n' $a | grep -qxF "$EXPECT_IP"; then pass "$DOMAIN points to $EXPECT_IP"; else fail "$DOMAIN does not point to $EXPECT_IP (found: ${a:-nothing})"; fi
  progress 5/7 "ports 80/443"
  local busy; busy="$(ss -ltnH '( sport = :80 or sport = :443 )' 2>/dev/null | wc -l)"
  if [ "$busy" = 0 ]; then pass "ports 80/443 free"
  elif [ "$(state_get phase)" = APPLIED ]; then info "ports 80/443 in use by the panel edge (already applied)"
  else fail "ports 80/443 already in use by another program"; fi
  progress 6/7 "resources"
  local disk; disk="$(df -Pm "${R:-/}" 2>/dev/null | awk 'NR==2{print $4}')"
  if [ "${disk:-0}" -ge 3000 ]; then pass "disk free ${disk} MB"; else fail "need at least 3000 MB free disk (found ${disk:-?})"; fi
  progress 7/7 "previous runs"
  info "phase=$(state_get phase) commit=$(state_get commit)"
  [ -n "$(foreign_publishers)" ] && warn "other containers publish ports: $(foreign_publishers | tr '\n' ' ')"
  return 0
}

fetch_source() {
  local dst="$SRC_ROOT/$COMMIT" tmp
  if [ -d "$dst/.git" ] && [ "$(git -C "$dst" rev-parse HEAD 2>/dev/null)" = "$COMMIT" ] && [ -z "$(git -C "$dst" status --porcelain 2>/dev/null)" ]; then
    pass "source $COMMIT already present and unchanged"; return 0
  fi
  mkdir -p "$SRC_ROOT"
  [ -e "$dst" ] && mv "$dst" "$BK/src-$COMMIT"
  tmp="$(mktemp -d "$SRC_ROOT/.fetch.XXXX")"
  git -C "$tmp" init -q && git -C "$tmp" fetch -q --depth 1 "$REPO_URL" "$COMMIT" && git -C "$tmp" -c advice.detachedHead=false checkout -q FETCH_HEAD || { mv "$tmp" "$BK/fetch-failed"; return 1; }
  [ "$(git -C "$tmp" rev-parse HEAD)" = "$COMMIT" ] || { echo "FAIL fetched commit does not match"; mv "$tmp" "$BK/fetch-mismatch"; return 1; }
  [ -f "$tmp/deploy/core/compose.yml" ] && [ -f "$tmp/panel/Dockerfile" ] || { echo "FAIL commit has no panel source"; mv "$tmp" "$BK/fetch-nosource"; return 1; }
  mv "$tmp" "$dst" && pass "source $COMMIT fetched and checked"
}

build_image() {
  local tag="nostek/panel:${COMMIT:0:12}" rev
  IMAGE="$tag"
  rev="$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$tag" 2>/dev/null)"
  if [ "$rev" = "$COMMIT" ]; then pass "image $tag already built from $COMMIT"; return 0; fi
  info "building $tag (a few minutes)"
  docker build -q --build-arg "VERSION=${COMMIT:0:12}" --build-arg "REVISION=$COMMIT" -t "$tag" "$SRC_ROOT/$COMMIT/panel" >/dev/null || return 1
  [ "$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$tag" 2>/dev/null)" = "$COMMIT" ] || return 1
  pass "image $tag built"
}

write_core() {
  mkdir -p "$CORE"
  cp "$SRC_ROOT/$COMMIT/deploy/core/compose.yml" "$CORE/compose.yml"
  ( umask 077
    { echo "# written by $STEP $(now_wib)"
      echo "PANEL_DOMAIN=$DOMAIN"
      echo "PANEL_IMAGE=$IMAGE"
      echo "PANEL_HOSTNAME=$(hostname)"
      echo "NOSTEK_DATA=$NOSTEK"
      echo "NOSTEK_STATE=$VARLIB"
      [ -n "$ACME_CA" ] && echo "ACME_CA_SERVER=$ACME_CA"
    } > "$CORE/.env" )
  chmod 600 "$CORE/.env"
  mkdir -p "$NOSTEK/panel" "$NOSTEK/edge/acme" "$VARLIB/backup"
  chown "$PANEL_UID:$PANEL_UID" "$NOSTEK/panel" "$NOSTEK/edge/acme"
  chmod 700 "$NOSTEK/panel" "$NOSTEK/edge/acme"
  pass "core files in $CORE (.env 600, data dirs owned by the panel user)"
}

wait_healthy() {
  local e p
  for _ in $(seq 1 45); do
    e="$(svc_state edge)"; p="$(svc_state panel)"
    [ "$e" = "running healthy" ] && [ "$p" = "running healthy" ] && { pass "edge and panel healthy"; return 0; }
    sleep 2
  done
  echo "FAIL not healthy: edge=$e panel=$p"; return 1
}

rollback() {
  echo "INFO rolling back: $*"
  [ -f "$CORE/compose.yml" ] && dc down --remove-orphans >/dev/null 2>&1
  if [ -d "$CORE" ]; then mv "$CORE" "$BK/core-failed"; fi
  if [ -d "$BK/core" ]; then
    cp -a "$BK/core" "$CORE" && dc up -d >/dev/null 2>&1 && echo "INFO previous core stack restored"
  fi
  state_set phase "${PREV_PHASE:-}"
  die "$*"
}

apply() {
  preflight
  [ "$FAILS" = 0 ] || die "preflight failed ($FAILS)"
  PREV_PHASE="$(state_get phase)"
  case "$PREV_PHASE" in ""|REVERTED) ;; *) refuse "already applied (phase=$PREV_PHASE); use VERIFY or REVERT" ;; esac
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)"; mkdir -p "$BK"
  progress A1/5 "fetch source $COMMIT"; fetch_source || die "cannot fetch source $COMMIT"
  progress A2/5 "build panel image"; build_image || die "image build failed"
  progress A3/5 "core files"
  if [ -d "$CORE" ]; then mv "$CORE" "$BK/core"; info "previous core moved aside to $BK/core"; fi
  write_core
  state_set phase APPLYING; state_set commit "$COMMIT"
  progress A4/5 "start edge, socket proxy, panel"
  dc up -d --remove-orphans >/dev/null 2>&1 || rollback "compose up failed"
  wait_healthy || rollback "containers did not become healthy"
  local f; f="$(foreign_publishers)"
  [ -z "$f" ] || rollback "containers other than edge publish ports: $f"
  pass "only edge publishes ports"
  local reach=""
  for _ in $(seq 1 15); do reach="$(https_check /healthz)"; [ "$reach" = 200 ] && break; sleep 2; done
  [ "$reach" = 200 ] || rollback "panel not reachable through the edge (https answered ${reach:-nothing})"
  pass "panel answers through the edge"
  progress A5/5 "HTTPS certificate (up to $((CERT_WAIT/60)) min)"
  local cert=pending
  for _ in $(seq 1 $((CERT_WAIT/5))); do cert_ok && { cert=valid; break; }; sleep 5; done
  [ "$TLS_TEST" = 1 ] && cert="test"
  if [ "$cert" = pending ]; then warn "certificate not issued yet; VERIFY will check again"; else pass "certificate $cert"; fi
  state_set phase APPLIED
  summary "PASS" "panel https://$DOMAIN running (image $IMAGE)" "certificate: $cert" "NEXT run SETUP-LINK to create the first admin"
  echo "RESULT $STEP APPLY PASS image=${COMMIT:0:12} cert=$cert"; exit 0
}

setup_link() {
  check_host
  [ "$(state_get phase)" = APPLIED ] || refuse "panel not applied (phase=$(state_get phase))"
  local out; out="$(dc exec -T panel /nostek-panel setup-link 2>&1)"
  case "$out" in
    *SETUP_LINK*) echo "$out" | grep -E '^SETUP_LINK '
      summary PASS "open the link in your browser within 30 minutes (one use)" "older setup links no longer work"
      echo "RESULT $STEP SETUP-LINK PASS valid_until=$(TZ=Asia/Jakarta date -d '+30 min' +%H:%M) WIB"; exit 0 ;;
    *"already exists"*) refuse "an admin already exists; use ADMIN-ROTATE" ;;
    *) die "setup link failed" ;;
  esac
}

admin_rotate() {
  check_host
  [ "$(state_get phase)" = APPLIED ] || refuse "panel not applied (phase=$(state_get phase))"
  local out; out="$(dc exec -T panel /nostek-panel admin-rotate "$EMAIL" 2>&1)"
  case "$out" in
    *REVEAL_LINK*) echo "$out" | grep -E '^REVEAL_LINK '
      summary PASS "new random password set; all sessions signed out" "open the link within 15 minutes to see it once"
      echo "RESULT $STEP ADMIN-ROTATE PASS valid_until=$(TZ=Asia/Jakarta date -d '+15 min' +%H:%M) WIB"; exit 0 ;;
    *"no such admin"*) refuse "no admin with that e-mail" ;;
    *) die "admin rotate failed" ;;
  esac
}

verify() {
  progress 1/6 "host identity"; check_host
  progress 2/6 "phase"; [ "$(state_get phase)" = APPLIED ] && pass "phase APPLIED (commit $(state_get commit))" || fail "phase is $(state_get phase), not APPLIED"
  progress 3/6 "containers"
  local s
  for s in edge panel; do [ "$(svc_state $s)" = "running healthy" ] && pass "$s running healthy" || fail "$s: $(svc_state $s)"; done
  case "$(svc_state docker-api)" in running*) pass "docker-api running" ;; *) fail "docker-api: $(svc_state docker-api)" ;; esac
  progress 4/6 "isolation"
  local f; f="$(foreign_publishers)"; [ -z "$f" ] && pass "only edge publishes ports" || fail "containers other than edge publish ports: $f"
  if dc exec -T docker-api wget -q -O /dev/null --post-data= http://127.0.0.1:2375/containers/create >/dev/null 2>&1; then fail "socket proxy accepts writes"; else pass "socket proxy refuses writes"; fi
  progress 5/6 "web"
  local h; h="$(curl -sS --noproxy '*' --max-time 8 -o /dev/null -w '%{http_code}' -H "Host: $DOMAIN" http://127.0.0.1/ 2>/dev/null)"
  [ "$h" = 301 ] || [ "$h" = 308 ] && pass "http redirects to https" || fail "http answered $h, expected redirect"
  [ "$(https_check /healthz)" = 200 ] && pass "panel healthy through the edge" || fail "panel not reachable through the edge"
  progress 6/6 "certificate"
  if [ "$TLS_TEST" = 1 ]; then info "certificate check skipped (test)"; CERT="test"
  elif cert_ok; then pass "certificate valid for $DOMAIN"; CERT=valid
  else fail "certificate for $DOMAIN not valid yet"; CERT=invalid; fi
  if [ "$FAILS" = 0 ]; then summary PASS "panel https://$DOMAIN" "certificate $CERT"; echo "RESULT $STEP VERIFY PASS cert=$CERT sisa_uji=0"; exit 0; fi
  summary FAIL "$FAILS check(s) failed"; echo "RESULT $STEP VERIFY FAIL fails=$FAILS"; exit 1
}

revert() {
  check_host
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)-revert"; mkdir -p "$BK"
  if [ -f "$CORE/compose.yml" ]; then dc down --remove-orphans >/dev/null 2>&1 && pass "core stack stopped"; fi
  if [ -d "$CORE" ]; then mv "$CORE" "$BK/core" && pass "core files moved aside to $BK/core"; fi
  state_set phase REVERTED
  summary PASS "panel stopped; ports 80/443 free" "panel data kept in $NOSTEK/panel (not deleted)"
  echo "RESULT $STEP REVERT PASS data_kept=yes"; exit 0
}

check_invocation
echo "INFO $STEP v$VERSION mode=$MODE $(now_wib)"
case "$MODE" in
  PREFLIGHT)
    preflight
    if [ "$FAILS" = 0 ]; then summary PASS "nothing changed" "NEXT APPLY (run inside tmux)"; echo "RESULT $STEP PREFLIGHT PASS"; exit 0; fi
    summary FAIL "$FAILS check(s) failed; nothing changed"; echo "RESULT $STEP PREFLIGHT FAIL fails=$FAILS"; exit 1 ;;
  APPLY) apply ;;
  SETUP-LINK) setup_link ;;
  ADMIN-ROTATE) admin_rotate ;;
  VERIFY) verify ;;
  REVERT) revert ;;
esac
