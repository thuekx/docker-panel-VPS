#!/usr/bin/env bash
# Nostek Panel - SITE-01: nostek-agent, the host service that creates and runs website stacks for the panel.
# The panel itself never gets Docker write access (SITE-DOCKER-1 = A); the agent accepts only fixed actions on a
# Unix socket that only the panel's user (uid 65532) and root can open.
# Modes: PREFLIGHT (read-only) | APPLY (install or update) | VERIFY (read-only) | REVERT
# Usage: sudo bash site-01-agent.sh MODE --ack SITE-01 --expect-ip ADDR --expect-host NAME --domain PANEL_DOMAIN
#        --preview-zone ZONE (e.g. preview.example.com; a wildcard A record *.ZONE must point to this server)
#        APPLY also: --src-commit SHA (40 hex; the repository commit to build the agent from)
# Websites keep running during APPLY and REVERT; nothing here deletes a website or its data.
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="SITE-01"; export SITE01_PKG_VERSION="1"
R="${SITE01_TEST_ROOT:-}"
NOSTEK="$R/srv/nostek"
SRC_ROOT="$NOSTEK/src"
VARLIB="$R/var/lib/nostek"
STATE_DIR="$VARLIB/site-01"
PANEL01_STATE="$VARLIB/panel-01/state"
BACKUP_ROOT="$R/var/backups/nostek/site-01"
BIN="$R/usr/local/sbin/nostek-agent"
CONF_DIR="$R/etc/nostek-agent"
CONF="$CONF_DIR/config.json"
UNIT_DIR="$R/etc/systemd/system"
RUN_DIR="$R/run/nostek-agent"
SOCK="$RUN_DIR/agent.sock"
LOG_DIR="$R/var/log/nostek-agent"
AGENT_HOME="$R/var/lib/nostek-agent"
REPO_URL="https://github.com/thuekx/docker-panel-VPS.git"
SYSTEMD=1
if [ -n "$R" ]; then   # test hooks: only active with a test root
  REPO_URL="${SITE01_TEST_REPO:-$REPO_URL}"; SYSTEMD=0   # the agent runs as a plain background process
fi
PANEL_UID=65532

MODE="${1:-}"; shift || true
ACK=""; EXPECT_IP=""; EXPECT_HOST=""; DOMAIN=""; ZONE=""; COMMIT=""; AUTO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --expect-host) EXPECT_HOST="${2:-}"; shift 2 ;;
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --preview-zone) ZONE="${2:-}"; shift 2 ;;
    --src-commit) COMMIT="${2:-}"; shift 2 ;;
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
HOSTRE='^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$'

check_invocation() {
  case "$MODE" in PREFLIGHT|APPLY|VERIFY|REVERT) ;; *) refuse "mode must be PREFLIGHT|APPLY|VERIFY|REVERT" ;; esac
  [ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
  [ "$(id -u)" = "0" ] || refuse "run with sudo"
  if [ "$AUTO" = "0" ] && [ -z "${SUDO_USER:-}" ]; then refuse "no root shell: run from your normal user with sudo"; fi
  [ -n "$EXPECT_IP" ] && [ -n "$EXPECT_HOST" ] || refuse "--expect-ip and --expect-host are required"
  [[ "$EXPECT_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || refuse "--expect-ip must be an IPv4 address"
  [[ "$DOMAIN" =~ $HOSTRE ]] || refuse "--domain must be the panel host name, lower case"
  [[ "$ZONE" =~ $HOSTRE ]] || refuse "--preview-zone must be a lower-case zone such as preview.example.com"
  [ "$ZONE" != "$DOMAIN" ] || refuse "--preview-zone must differ from the panel domain"
  if [ "$MODE" = APPLY ]; then [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "--src-commit must be a full 40-character commit id"; fi
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
panel_phase() { grep -E '^phase=' "$PANEL01_STATE" 2>/dev/null | tail -n1 | cut -d= -f2; }
agent_call() { # $1 path; prints body. Uses the socket as root.
  curl -fsS --noproxy '*' --max-time 10 --unix-socket "$SOCK" "http://agent$1" 2>/dev/null
}
agent_health() { agent_call /v1/health | grep -q '"ok":true'; }
preview_dns() { # a random name under the zone must resolve to this server (wildcard record)
  local n a; n="np-check-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n').$ZONE"
  a="$(getent ahostsv4 "$n" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')"
  printf '%s\n' $a | grep -qxF "$EXPECT_IP"
}

# ---------- PREFLIGHT ----------
preflight() {
  progress 1/5 "host identity"; check_host
  progress 2/5 "PANEL-01 state"
  if [ "$(panel_phase)" = APPLIED ]; then pass "PANEL-01 applied (edge proxy running)"; else fail "PANEL-01 is not applied (phase=$(panel_phase)); install the panel first"; fi
  progress 3/5 "docker"
  if docker info >/dev/null 2>&1; then pass "docker engine $(docker version --format '{{.Server.Version}}' 2>/dev/null)"; else fail "docker engine not running"; fi
  if docker compose version >/dev/null 2>&1; then pass "docker compose $(docker compose version --short 2>/dev/null)"; else fail "docker compose plugin missing"; fi
  if docker network inspect nostek-edge >/dev/null 2>&1; then pass "network nostek-edge present"; else fail "network nostek-edge missing (PANEL-01 core not running)"; fi
  command -v git >/dev/null 2>&1 && pass "git present" || fail "git missing (apt-get install git)"
  progress 4/5 "preview addresses *.$ZONE"
  if preview_dns; then pass "*.$ZONE points to $EXPECT_IP"; else warn "*.$ZONE does not point to $EXPECT_IP yet: new websites need a domain that points here"; fi
  progress 5/5 "resources"
  local disk mem; disk="$(df -Pm "${R:-/}" 2>/dev/null | awk 'NR==2{print $4}')"
  if [ "${disk:-0}" -ge 5000 ]; then pass "disk free ${disk} MB"; else fail "need at least 5000 MB free disk for websites (found ${disk:-?})"; fi
  mem="$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo 2>/dev/null)"
  if [ "${mem:-0}" -ge 1200 ]; then pass "memory available ${mem} MB"; else warn "memory available ${mem:-?} MB: each website needs up to 1 GB"; fi
  info "phase=$(state_get phase) release=$(state_get release)"
  return 0
}

fetch_source() {
  local dst="$SRC_ROOT/$COMMIT" tmp
  if [ -d "$dst/.git" ] && [ "$(git -C "$dst" rev-parse HEAD 2>/dev/null)" = "$COMMIT" ] && [ -z "$(git -C "$dst" status --porcelain 2>/dev/null)" ]; then
    pass "source $COMMIT already present and unchanged"
  else
    mkdir -p "$SRC_ROOT"
    [ -e "$dst" ] && mv "$dst" "$BK/src-$COMMIT"
    tmp="$(mktemp -d "$SRC_ROOT/.fetch.XXXX")"
    git -C "$tmp" init -q && git -C "$tmp" fetch -q --depth 1 "$REPO_URL" "$COMMIT" && git -C "$tmp" -c advice.detachedHead=false checkout -q FETCH_HEAD || { mv "$tmp" "$BK/fetch-failed"; return 1; }
    [ "$(git -C "$tmp" rev-parse HEAD)" = "$COMMIT" ] || { echo "FAIL fetched commit does not match"; mv "$tmp" "$BK/fetch-mismatch"; return 1; }
    mv "$tmp" "$dst" && pass "source $COMMIT fetched and checked"
  fi
  [ -f "$dst/agent/Dockerfile" ] || { echo "FAIL commit has no agent source"; return 1; }
  grep -qxE '[0-9]+\.[0-9]+\.[0-9]+' "$dst/VERSION" 2>/dev/null || { echo "FAIL commit has no release VERSION file"; return 1; }
}

build_agent() { # builds in Docker, copies the static binary out to $BK/new
  local tag cid
  REL="v$(tr -d '[:space:]' < "$SRC_ROOT/$COMMIT/VERSION")"; tag="nostek/agent-build:$REL"
  if [ "$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$tag" 2>/dev/null)" = "$COMMIT" ]; then
    pass "agent build $tag already built from $COMMIT"
  else
    info "building agent $REL (a few minutes)"
    docker build -q --build-arg "VERSION=$REL" --build-arg "REVISION=$COMMIT" -t "$tag" "$SRC_ROOT/$COMMIT/agent" >/dev/null || return 1
  fi
  mkdir -p "$BK/new"
  cid="$(docker create "$tag")" || return 1
  docker cp "$cid:/nostek-agent" "$BK/new/nostek-agent" >/dev/null; local rc=$?
  docker rm "$cid" >/dev/null 2>&1
  [ $rc = 0 ] || return 1
  chmod 0755 "$BK/new/nostek-agent"
  [ "$("$BK/new/nostek-agent" version 2>/dev/null)" = "$REL" ] || { echo "FAIL built agent does not report $REL"; return 1; }
  pass "agent $REL built"
}

write_config() { # $1 = target file
  cat > "$1" <<EOF
{
  "data": "$NOSTEK",
  "public_ipv4": "$EXPECT_IP",
  "panel_domain": "$DOMAIN",
  "preview_zone": "$ZONE",
  "socket": "$SOCK",
  "socket_gid": $PANEL_UID,
  "allowed_uids": [0, $PANEL_UID],
  "audit": "$LOG_DIR/audit.log"
}
EOF
  chmod 0640 "$1"
}

write_units() {
  mkdir -p "$UNIT_DIR"
  cat > "$UNIT_DIR/nostek-agent.service" <<EOF
# Installed by Nostek Panel SITE-01. The only service allowed to change Docker for the panel.
[Unit]
Description=Nostek agent (website stacks for Nostek Panel)
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=simple
Environment=NOSTEK_AGENT_CONFIG=$CONF
Environment=DOCKER_CONFIG=$AGENT_HOME/docker
ExecStartPre=/usr/local/sbin/nostek-agent check-config
ExecStart=/usr/local/sbin/nostek-agent serve
Restart=on-failure
RestartSec=3
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
UMask=0022

[Install]
WantedBy=multi-user.target
EOF
  cat > "$UNIT_DIR/nostek-agent-purge.service" <<EOF
# Installed by Nostek Panel SITE-01: removes websites that have been in the trash for more than 7 days.
[Unit]
Description=Nostek agent: empty website trash older than 7 days

[Service]
Type=oneshot
Environment=NOSTEK_AGENT_CONFIG=$CONF
ExecStart=/usr/local/sbin/nostek-agent purge
EOF
  cat > "$UNIT_DIR/nostek-agent-purge.timer" <<EOF
[Unit]
Description=Nostek agent: daily trash clean-up

[Timer]
OnCalendar=*-*-* 04:30:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

start_agent() {
  if [ "$SYSTEMD" = 1 ]; then
    systemctl daemon-reload && systemctl enable -q nostek-agent.service nostek-agent-purge.timer && systemctl restart nostek-agent.service && systemctl start nostek-agent-purge.timer
  else
    stop_agent
    NOSTEK_AGENT_CONFIG="$CONF" DOCKER_CONFIG="$AGENT_HOME/docker" nohup "$BIN" serve >> "$LOG_DIR/test-agent.log" 2>&1 &
    echo $! > "$STATE_DIR/test-pid"
  fi
}
stop_agent() {
  if [ "$SYSTEMD" = 1 ]; then
    systemctl disable -q --now nostek-agent-purge.timer >/dev/null 2>&1
    systemctl disable -q --now nostek-agent.service >/dev/null 2>&1
  elif [ -f "$STATE_DIR/test-pid" ]; then
    kill "$(cat "$STATE_DIR/test-pid")" >/dev/null 2>&1; rm -f "$STATE_DIR/test-pid"; sleep 0.5
  fi
  return 0
}
wait_agent() { local _; for _ in $(seq 1 20); do agent_health && return 0; sleep 1; done; return 1; }

rollback() {
  echo "FAIL $1"; info "rolling back"
  stop_agent
  for f in usr-local-sbin-nostek-agent:"$BIN" config.json:"$CONF" nostek-agent.service:"$UNIT_DIR/nostek-agent.service" \
           nostek-agent-purge.service:"$UNIT_DIR/nostek-agent-purge.service" nostek-agent-purge.timer:"$UNIT_DIR/nostek-agent-purge.timer"; do
    local src="$BK/old/${f%%:*}" dst="${f#*:}"
    if [ -f "$src" ]; then cp -p "$src" "$dst"; else [ -f "$dst" ] && mv "$dst" "$BK/rolled-back-$(basename "$dst")"; fi
  done
  if [ -f "$BK/old/usr-local-sbin-nostek-agent" ]; then start_agent; wait_agent && info "previous agent running again"; fi
  state_set phase "${PREV_PHASE:-}"
  summary FAIL "$1" "rolled back; websites were not touched"
  echo "RESULT $STEP $MODE FAIL $1"; exit 1
}

apply() {
  preflight
  [ "$FAILS" = 0 ] || die "preflight failed ($FAILS); nothing changed"
  PREV_PHASE="$(state_get phase)"
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)-apply"; mkdir -p "$BK/old"
  progress A1/4 "fetch and build agent from $COMMIT"
  fetch_source || die "cannot fetch source $COMMIT; nothing changed"
  build_agent || die "agent build failed; nothing changed"
  progress A2/4 "install files"
  [ -f "$BIN" ] && cp -p "$BIN" "$BK/old/usr-local-sbin-nostek-agent"
  [ -f "$CONF" ] && cp -p "$CONF" "$BK/old/config.json"
  for u in nostek-agent.service nostek-agent-purge.service nostek-agent-purge.timer; do [ -f "$UNIT_DIR/$u" ] && cp -p "$UNIT_DIR/$u" "$BK/old/$u"; done
  state_set phase APPLYING
  mkdir -p "$(dirname "$BIN")" "$CONF_DIR" "$LOG_DIR" "$AGENT_HOME/docker" "$NOSTEK/sites" "$RUN_DIR"
  mkdir -p "$NOSTEK/trash"; chmod 0700 "$NOSTEK/trash" "$LOG_DIR" "$AGENT_HOME"
  chmod 0755 "$CONF_DIR" "$NOSTEK/sites"
  install -m 0755 "$BK/new/nostek-agent" "$BIN.new" && mv "$BIN.new" "$BIN" || rollback "cannot install the agent binary"
  write_config "$CONF.new" && mv "$CONF.new" "$CONF" || rollback "cannot write the agent config"
  NOSTEK_AGENT_CONFIG="$CONF" "$BIN" check-config >/dev/null || rollback "agent refuses its config"
  write_units || rollback "cannot write the systemd units"
  pass "agent files installed"
  progress A3/4 "start agent"
  start_agent || rollback "agent did not start"
  wait_agent || rollback "agent does not answer on $SOCK"
  pass "agent $REL answers on its socket"
  [ "$(stat -c '%a %g' "$SOCK" 2>/dev/null)" = "660 $PANEL_UID" ] || rollback "socket permissions wrong ($(stat -c '%a %g' "$SOCK" 2>/dev/null))"
  pass "socket open only to root and the panel user"
  progress A4/4 "preview addresses"
  local pdns=ok
  if preview_dns; then pass "*.$ZONE points to $EXPECT_IP"; else pdns=missing; warn "*.$ZONE does not point here yet"; fi
  state_set phase APPLIED; state_set commit "$COMMIT"; state_set release "$REL"; state_set zone "$ZONE"
  summary PASS "nostek-agent $REL running" "preview addresses: *.$ZONE ($pdns)" "NEXT: update the panel (PANEL-01 UPDATE) so it connects to the agent"
  echo "RESULT $STEP APPLY PASS version=$REL preview_dns=$pdns"; exit 0
}

verify() {
  progress 1/6 "host identity"; check_host
  progress 2/6 "state"
  [ "$(state_get phase)" = APPLIED ] && pass "SITE-01 applied ($(state_get release))" || fail "SITE-01 not applied (phase=$(state_get phase))"
  progress 3/6 "agent service"
  if [ "$SYSTEMD" = 1 ]; then
    systemctl is-active -q nostek-agent.service && pass "nostek-agent.service active" || fail "nostek-agent.service not active"
    systemctl is-enabled -q nostek-agent.service && pass "starts at boot" || fail "nostek-agent.service not enabled"
    systemctl is-active -q nostek-agent-purge.timer && pass "trash clean-up timer active" || fail "nostek-agent-purge.timer not active"
  fi
  local v; v="$("$BIN" version 2>/dev/null)"
  [ -n "$v" ] && [ "$v" = "$(state_get release)" ] && pass "binary version $v" || fail "binary version '$v' does not match $(state_get release)"
  NOSTEK_AGENT_CONFIG="$CONF" "$BIN" check-config >/dev/null 2>&1 && pass "config valid" || fail "config invalid"
  progress 4/6 "socket"
  agent_health && pass "agent answers" || fail "agent does not answer on $SOCK"
  [ "$(stat -c '%a %u %g' "$SOCK" 2>/dev/null)" = "660 0 $PANEL_UID" ] && pass "socket 660 root:$PANEL_UID" || fail "socket permissions wrong"
  [ "$(stat -c '%a' "$RUN_DIR" 2>/dev/null)" = 750 ] && pass "socket folder 750" || fail "socket folder not 750"
  if command -v setpriv >/dev/null 2>&1; then
    if setpriv --reuid 65534 --regid 65534 --clear-groups curl -fsS --max-time 5 --unix-socket "$SOCK" http://agent/v1/health >/dev/null 2>&1; then
      fail "another user can call the agent"
    else pass "other users cannot call the agent"; fi
  fi
  progress 5/6 "fixed actions only"
  local code; code="$(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' --max-time 5 --unix-socket "$SOCK" -X POST http://agent/v1/containers/create 2>/dev/null)"
  [ "$code" = 404 ] || [ "$code" = 405 ] && pass "agent refuses actions outside its list" || fail "agent answered $code to a raw Docker call"
  progress 6/6 "preview addresses"
  if preview_dns; then pass "*.$ZONE points to $EXPECT_IP"; else warn "*.$ZONE does not point to $EXPECT_IP"; fi
  local n; n="$(agent_call /v1/sites | grep -o '"id":' | wc -l)"
  info "websites: $n"
  if [ "$FAILS" = 0 ]; then summary PASS "nostek-agent $(state_get release)" "websites: $n"; echo "RESULT $STEP VERIFY PASS version=$(state_get release) sites=$n sisa_uji=0"; exit 0; fi
  summary FAIL "$FAILS checks failed"; echo "RESULT $STEP VERIFY FAIL fails=$FAILS"; exit 1
}

revert() {
  check_host
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)-revert"; mkdir -p "$BK"
  stop_agent; pass "agent stopped"
  for f in "$BIN" "$CONF" "$UNIT_DIR/nostek-agent.service" "$UNIT_DIR/nostek-agent-purge.service" "$UNIT_DIR/nostek-agent-purge.timer"; do
    [ -f "$f" ] && mv "$f" "$BK/$(basename "$f")"
  done
  [ "$SYSTEMD" = 1 ] && systemctl daemon-reload
  pass "agent files moved aside to $BK"
  state_set phase REVERTED
  local n; n="$(find "$NOSTEK/sites" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)"
  summary PASS "agent removed; $n website(s) keep running but cannot be changed from the panel" "undo: run APPLY again"
  echo "RESULT $STEP REVERT PASS sites_kept=$n"; exit 0
}

check_invocation
case "$MODE" in
  PREFLIGHT) preflight; if [ "$FAILS" = 0 ]; then summary PASS "ready for APPLY"; echo "RESULT $STEP PREFLIGHT PASS"; exit 0; fi
             summary FAIL "$FAILS checks failed"; echo "RESULT $STEP PREFLIGHT FAIL fails=$FAILS"; exit 1 ;;
  APPLY) apply ;;
  VERIFY) verify ;;
  REVERT) revert ;;
esac
