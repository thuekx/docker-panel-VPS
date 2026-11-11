#!/usr/bin/env bash
# Nostek Panel - HOST-01: own login user + new SSH port, lock-out safe.
# Modes: PREFLIGHT (read-only) | APPLY | CONFIRM | FINALIZE | REVERT | STATUS
# Usage: sudo bash host-01-user-ssh.sh MODE --ack HOST-01 --user NAME --port N --expect-ip ADDR
#        [--old-user NAME] [--stdin]   (password for APPLY is read from stdin, never from argv)
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 077

STEP="HOST-01"
VERSION="1"
R="${HOST01_TEST_ROOT:-}"                     # test harness only; empty on a real host
STATE_DIR="$R/var/lib/nostek/host-01"
BACKUP_ROOT="$R/var/backups/nostek/host-01"
DROPIN="$R/etc/ssh/sshd_config.d/05-nostek-host01.conf"
TIMER_UNIT="nostek-host01-revert"
REVERT_SECONDS=600

MODE="${1:-}"; shift || true
ACK=""; USERNAME=""; PORT=""; EXPECT_IP=""; OLD_USER=""; USE_STDIN=0; AUTO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --user) USERNAME="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --old-user) OLD_USER="${2:-}"; shift 2 ;;
    --stdin) USE_STDIN=1; shift ;;
    --auto) AUTO=1; shift ;;
    *) echo "REFUSE unknown argument: $1"; exit 2 ;;
  esac
done

now_wib() { TZ=Asia/Jakarta date '+%Y-%m-%d %H:%M:%S WIB'; }
say() { echo "$*"; }
pass() { echo "PASS $*"; }
warn() { echo "WARN $*"; }
FAILS=0
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
refuse() { echo "REFUSE $*"; echo "RESULT $STEP $MODE REFUSE $*"; exit 3; }
progress() { echo "PROGRESS $1 $2"; }

wrong_host() {
  local h msg
  h="$(hostname 2>/dev/null || echo unknown)"
  msg="❌ WRONG HOST: this is $h — run $STEP on the host with address $EXPECT_IP; nothing done"
  echo "$msg"; echo "$msg" >&2
  { [ -w /dev/tty ] && echo "$msg" > /dev/tty; } 2>/dev/null || true
  echo "RESULT $STEP $MODE WRONG_HOST"
  exit 4
}

summary() {
  local status="$1"; shift
  echo "SUMMARY ===== $STEP $MODE: $status ($(now_wib)) ====="
  for l in "$@"; do echo "SUMMARY $l"; done
  echo "SUMMARY ======================================================="
}

state_get() { [ -f "$STATE_DIR/state" ] && grep -E "^$1=" "$STATE_DIR/state" | tail -n1 | cut -d= -f2-; }
state_set() {
  mkdir -p "$STATE_DIR"; touch "$STATE_DIR/state"
  grep -vE "^$1=" "$STATE_DIR/state" > "$STATE_DIR/state.tmp" || true
  echo "$1=$2" >> "$STATE_DIR/state.tmp"; mv "$STATE_DIR/state.tmp" "$STATE_DIR/state"
}

# ---------- common checks ----------
check_invocation() {
  case "$MODE" in PREFLIGHT|APPLY|CONFIRM|FINALIZE|REVERT|STATUS) ;; *) refuse "mode must be PREFLIGHT|APPLY|CONFIRM|FINALIZE|REVERT|STATUS" ;; esac
  [ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
  [ "$(id -u)" = "0" ] || refuse "run with sudo: sudo bash $0 $MODE --ack $STEP ..."
  if [ "$AUTO" = "0" ] && [ -z "${SUDO_USER:-}" ]; then refuse "no root shell: run from your normal user with sudo"; fi
  [[ "$USERNAME" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || refuse "--user must be a valid Linux user name"
  [[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1024 ] && [ "$PORT" -le 65535 ] || refuse "--port must be 1024-65535"
  [ -n "$EXPECT_IP" ] || refuse "--expect-ip is required"
}

check_host() {
  local ips pub
  ips="$(ip -o -4 addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1)"
  if printf '%s\n' "$ips" | grep -qxF "$EXPECT_IP"; then pass "host identity: $EXPECT_IP is a local address"; return 0; fi
  for url in https://api.ipify.org https://ifconfig.me/ip; do
    pub="$(curl -fsS --max-time 6 "$url" 2>/dev/null | tr -d '[:space:]')"
    if [ -n "$pub" ]; then
      if [ "$pub" = "$EXPECT_IP" ]; then pass "host identity: public address matches"; return 0; fi
      wrong_host
    fi
  done
  refuse "cannot prove host identity (no local match, public address lookup failed)"
}

SSH_MODE=""
detect_ssh_mode() {
  if systemctl is-enabled ssh.socket >/dev/null 2>&1 || systemctl is-active ssh.socket >/dev/null 2>&1; then SSH_MODE="socket"; else SSH_MODE="service"; fi
}

listening() { ss -ltnH "sport = :$1" 2>/dev/null | grep -q .; }
ufw_active() { command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; }
user_exists() { getent passwd "$1" >/dev/null 2>&1; }

reload_ssh() {
  systemctl daemon-reload || return 1
  if [ "$SSH_MODE" = "socket" ]; then
    systemctl restart ssh.socket || return 1
  else
    systemctl restart ssh || systemctl restart sshd || return 1
  fi
  sleep "${HOST01_SETTLE:-2}"
}

preflight_checks() {
  progress 1/9 "invocation + host identity"
  check_host
  progress 2/9 "operating system"
  local id ver
  id="$(. "$R/etc/os-release" 2>/dev/null; echo "${ID:-}")"; ver="$(. "$R/etc/os-release" 2>/dev/null; echo "${VERSION_ID:-}")"
  if [ "$id" = "ubuntu" ] && { [ "$ver" = "22.04" ] || [ "$ver" = "24.04" ]; }; then pass "os ubuntu $ver"; else fail "os must be Ubuntu 22.04 or 24.04 (found ${id:-?} ${ver:-?})"; fi
  OS_VER="$ver"
  progress 3/9 "required commands"
  local c miss=""
  for c in sshd ss systemctl systemd-run useradd usermod chpasswd passwd getent ip visudo; do command -v "$c" >/dev/null 2>&1 || miss="$miss $c"; done
  if [ -z "$miss" ]; then pass "commands present"; else fail "missing commands:$miss"; fi
  progress 4/9 "current sshd config"
  if sshd -t 2>/dev/null; then pass "sshd -t ok"; else fail "current sshd config is invalid (sshd -t)"; fi
  PW_AUTH="$(sshd -T 2>/dev/null | awk '$1=="passwordauthentication"{print $2}')"
  say "INFO effective PasswordAuthentication=${PW_AUTH:-unknown} ports=$(sshd -T 2>/dev/null | awk '$1=="port"{printf "%s ",$2}')"
  progress 5/9 "ssh start mode"
  detect_ssh_mode; pass "ssh mode $SSH_MODE"
  progress 6/9 "port $PORT"
  if [ "$PORT" = "22" ]; then fail "port must not be 22"; elif listening "$PORT" && [ "$(state_get phase)" = "" ]; then fail "port $PORT is already in use"; else pass "port $PORT free or ours"; fi
  listening 22 && pass "port 22 listening (current sessions safe)" || warn "port 22 not listening"
  progress 7/9 "user $USERNAME"
  if user_exists "$USERNAME"; then USER_EXISTS=yes; warn "user $USERNAME already exists (will be reused)"; else USER_EXISTS=no; pass "user $USERNAME does not exist yet"; fi
  progress 8/9 "firewall"
  if ufw_active; then UFW=active; pass "ufw active (rule for $PORT will be added)"; else UFW=inactive; warn "ufw inactive (host firewall comes in HOST-02); provider firewall must allow TCP $PORT"; fi
  progress 9/9 "previous runs"
  PHASE="$(state_get phase)"; say "INFO phase=${PHASE:-none}"
  if systemctl is-active "$TIMER_UNIT.timer" >/dev/null 2>&1; then warn "revert timer is running"; fi
}

backup_now() {
  local ts dir
  ts="$(date -u +%Y%m%dT%H%M%SZ)"; dir="$BACKUP_ROOT/$ts-$MODE"
  mkdir -p "$dir" || return 1
  cp -a "$R/etc/ssh/sshd_config.d" "$dir/sshd_config.d" || return 1
  [ -d "$R/etc/ufw" ] && { for f in user.rules user6.rules; do [ -f "$R/etc/ufw/$f" ] && cp -a "$R/etc/ufw/$f" "$dir/"; done; }
  state_set backup "$dir"
  echo "$dir"
}

schedule_revert() {
  mkdir -p "$STATE_DIR"; cp "$0" "$STATE_DIR/host-01-user-ssh.sh"; chmod 700 "$STATE_DIR/host-01-user-ssh.sh"
  systemctl stop "$TIMER_UNIT.timer" "$TIMER_UNIT.service" >/dev/null 2>&1 || true
  systemctl reset-failed "$TIMER_UNIT.service" >/dev/null 2>&1 || true
  systemd-run --unit="$TIMER_UNIT" --on-active="$REVERT_SECONDS" /bin/bash "$STATE_DIR/host-01-user-ssh.sh" REVERT --ack "$STEP" --auto --user "$USERNAME" --port "$PORT" --expect-ip "$EXPECT_IP" >/dev/null 2>&1
}

# ---------- REVERT ----------
do_revert() {
  local dir; dir="$(state_get backup)"
  [ -n "$dir" ] && [ -d "$dir/sshd_config.d" ] || { echo "FAIL no backup recorded; nothing reverted"; return 1; }
  local aside; aside="$BACKUP_ROOT/reverted-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$aside"
  mv "$R/etc/ssh/sshd_config.d" "$aside/sshd_config.d" && cp -a "$dir/sshd_config.d" "$R/etc/ssh/sshd_config.d"
  for f in user.rules user6.rules; do
    if [ -f "$dir/$f" ]; then mv "$R/etc/ufw/$f" "$aside/$f" 2>/dev/null; cp -a "$dir/$f" "$R/etc/ufw/$f"; fi
  done
  ufw_active && ufw reload >/dev/null 2>&1
  local old; old="$(state_get old_user)"
  if [ -n "$old" ] && [ "$(state_get old_locked)" = "yes" ]; then
    passwd -u "$old" >/dev/null 2>&1
    usermod -s "$(state_get old_shell)" "$old" >/dev/null 2>&1
    [ -f "$R/home/$old/.ssh/authorized_keys.disabled-host01" ] && mv "$R/home/$old/.ssh/authorized_keys.disabled-host01" "$R/home/$old/.ssh/authorized_keys"
    [ -f "$dir/90-cloud-init-users" ] && cp -a "$dir/90-cloud-init-users" "$R/etc/sudoers.d/90-cloud-init-users"
    state_set old_locked no
  fi
  detect_ssh_mode; reload_ssh
  state_set phase "$(state_get prev_phase)"
  echo "PASS reverted from $dir (current files moved aside to $aside)"
}

# ---------- main ----------
check_invocation
echo "INFO $STEP v$VERSION mode=$MODE time=$(now_wib)"
OS_VER=""; PW_AUTH=""; USER_EXISTS=""; UFW=""; PHASE=""

case "$MODE" in
PREFLIGHT|STATUS)
  preflight_checks
  if [ "$FAILS" -eq 0 ]; then
    summary PASS "os=$OS_VER ssh_mode=$SSH_MODE user_exists=$USER_EXISTS ufw=$UFW phase=${PHASE:-none}" "NEXT APPLY (needs the new password via --stdin)"
    echo "RESULT $STEP $MODE PASS os=$OS_VER ssh_mode=$SSH_MODE user_exists=$USER_EXISTS ufw=$UFW pw_auth=${PW_AUTH:-unknown} phase=${PHASE:-none}"; exit 0
  fi
  summary FAIL "$FAILS check(s) failed, nothing changed"; echo "RESULT $STEP $MODE FAIL fails=$FAILS"; exit 1 ;;

APPLY)
  preflight_checks
  [ "$FAILS" -eq 0 ] || { summary FAIL "preflight failed, nothing changed"; echo "RESULT $STEP APPLY FAIL preflight fails=$FAILS"; exit 1; }
  [ -z "$PHASE" ] || refuse "already applied (phase=$PHASE); use STATUS, CONFIRM or REVERT"
  PW=""
  if [ "$USE_STDIN" = "1" ]; then IFS= read -r PW || true; fi
  if [ "$USER_EXISTS" = "no" ] && [ -z "$PW" ]; then refuse "new user needs a password via --stdin"; fi
  if [ -n "$PW" ] && [ "${#PW}" -lt 12 ]; then PW=""; refuse "password must be at least 12 characters"; fi
  progress 1/6 "backup"
  BK="$(backup_now)" || { echo "FAIL backup"; echo "RESULT $STEP APPLY FAIL backup"; exit 1; }
  pass "backup at $BK"
  state_set prev_phase ""
  progress 2/6 "user $USERNAME"
  if [ "$USER_EXISTS" = "no" ]; then useradd -m -s /bin/bash -G sudo "$USERNAME" || { echo "FAIL useradd"; echo "RESULT $STEP APPLY FAIL useradd"; exit 1; }; state_set created_user yes
  else usermod -aG sudo "$USERNAME"; fi
  if [ -n "$PW" ]; then printf '%s:%s\n' "$USERNAME" "$PW" | chpasswd || { PW=""; echo "FAIL chpasswd"; echo "RESULT $STEP APPLY FAIL chpasswd"; exit 1; }; pass "password set (not shown)"; fi
  PW=""
  progress 3/6 "sshd drop-in (port 22 + $PORT)"
  printf '# Nostek Panel HOST-01 (managed)\nPort 22\nPort %s\nPasswordAuthentication yes\nKbdInteractiveAuthentication no\nPermitRootLogin no\n' "$PORT" > "$DROPIN"
  chmod 644 "$DROPIN"
  if ! sshd -t 2>/dev/null; then echo "FAIL new sshd config invalid, rolling back"; do_revert; echo "RESULT $STEP APPLY FAIL sshd_config rolled_back"; exit 1; fi
  pass "sshd -t ok with new port"
  progress 4/6 "firewall"
  if [ "$UFW" = "active" ]; then ufw allow "$PORT/tcp" comment "nostek HOST-01" >/dev/null && pass "ufw allows $PORT/tcp"; else warn "ufw inactive, no host rule needed now"; fi
  progress 5/6 "safety net + restart ssh"
  state_set phase APPLIED_PENDING; state_set user "$USERNAME"; state_set port "$PORT"
  schedule_revert || { echo "FAIL could not schedule auto-revert, rolling back"; do_revert; echo "RESULT $STEP APPLY FAIL timer rolled_back"; exit 1; }
  pass "auto-revert in $((REVERT_SECONDS/60)) min unless CONFIRM"
  if ! reload_ssh; then echo "FAIL ssh restart, rolling back"; do_revert; echo "RESULT $STEP APPLY FAIL restart rolled_back"; exit 1; fi
  progress 6/6 "verify listeners"
  if listening 22 && listening "$PORT"; then pass "ssh listening on 22 and $PORT"
  else echo "FAIL ssh not listening on both ports, rolling back"; systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1; do_revert; echo "RESULT $STEP APPLY FAIL listen rolled_back"; exit 1; fi
  DEADLINE="$(TZ=Asia/Jakarta date -d "+$((REVERT_SECONDS/60)) min" '+%H:%M WIB' 2>/dev/null || echo '10 min')"
  summary "PASS (waiting for CONFIRM)" "user $USERNAME ready, ssh on 22 and $PORT" "auto-revert at $DEADLINE if not confirmed" "NEXT open a NEW terminal: ssh -p $PORT $USERNAME@$EXPECT_IP and run CONFIRM there"
  echo "RESULT $STEP APPLY PASS user=$USERNAME port=$PORT ssh_mode=$SSH_MODE ufw=$UFW revert_at=$DEADLINE"; exit 0 ;;

CONFIRM)
  check_host
  [ "${SUDO_USER:-}" = "$USERNAME" ] || refuse "CONFIRM must run in a NEW session logged in as $USERNAME (now: ${SUDO_USER:-none})"
  PHASE="$(state_get phase)"
  case "$PHASE" in APPLIED_PENDING|FINALIZED_PENDING) ;; *) refuse "nothing to confirm (phase=${PHASE:-none})" ;; esac
  if ! ss -tnH state established "( sport = :$PORT )" 2>/dev/null | grep -q .; then refuse "no SSH session on port $PORT found; log in with ssh -p $PORT first"; fi
  pass "session on port $PORT as $USERNAME"
  systemctl stop "$TIMER_UNIT.timer" "$TIMER_UNIT.service" >/dev/null 2>&1 || true
  if systemctl is-active "$TIMER_UNIT.timer" >/dev/null 2>&1; then echo "FAIL revert timer still active"; echo "RESULT $STEP CONFIRM FAIL timer"; exit 1; fi
  pass "auto-revert cancelled"
  if [ "$PHASE" = "APPLIED_PENDING" ]; then state_set phase APPLIED; NEXTL="NEXT FINALIZE (close port 22, lock the old user)"; else state_set phase DONE; NEXTL="NEXT nothing; HOST-01 complete"; fi
  summary PASS "phase=$(state_get phase)" "$NEXTL"
  echo "RESULT $STEP CONFIRM PASS phase=$(state_get phase)"; exit 0 ;;

FINALIZE)
  check_host
  [ "${SUDO_USER:-}" = "$USERNAME" ] || refuse "FINALIZE must run as $USERNAME on port $PORT (now: ${SUDO_USER:-none})"
  [[ "$OLD_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || refuse "--old-user is required"
  [ "$OLD_USER" != "$USERNAME" ] || refuse "--old-user must differ from --user"
  [ "$(state_get phase)" = "APPLIED" ] || refuse "run APPLY and CONFIRM first (phase=$(state_get phase))"
  user_exists "$OLD_USER" || refuse "old user $OLD_USER not found"
  detect_ssh_mode; UFW=inactive; ufw_active && UFW=active
  progress 1/5 "backup"
  BK="$(backup_now)" || { echo "FAIL backup"; echo "RESULT $STEP FINALIZE FAIL backup"; exit 1; }
  [ -f "$R/etc/sudoers.d/90-cloud-init-users" ] && cp -a "$R/etc/sudoers.d/90-cloud-init-users" "$BK/"
  pass "backup at $BK"
  state_set prev_phase APPLIED
  progress 2/5 "ssh only on $PORT"
  printf '# Nostek Panel HOST-01 (managed)\nPort %s\nPasswordAuthentication yes\nKbdInteractiveAuthentication no\nPermitRootLogin no\n' "$PORT" > "$DROPIN"
  if ! sshd -t 2>/dev/null; then echo "FAIL sshd config invalid, rolling back"; do_revert; echo "RESULT $STEP FINALIZE FAIL sshd_config rolled_back"; exit 1; fi
  if [ "$UFW" = "active" ]; then ufw delete allow 22/tcp >/dev/null 2>&1; ufw delete allow OpenSSH >/dev/null 2>&1; pass "ufw rules for 22 removed"; fi
  progress 3/5 "lock old user $OLD_USER (not deleted)"
  state_set old_user "$OLD_USER"; state_set old_shell "$(getent passwd "$OLD_USER" | cut -d: -f7)"
  passwd -l "$OLD_USER" >/dev/null 2>&1; usermod -s /usr/sbin/nologin "$OLD_USER" >/dev/null 2>&1
  [ -f "$R/home/$OLD_USER/.ssh/authorized_keys" ] && mv "$R/home/$OLD_USER/.ssh/authorized_keys" "$R/home/$OLD_USER/.ssh/authorized_keys.disabled-host01"
  [ -f "$R/etc/sudoers.d/90-cloud-init-users" ] && mv "$R/etc/sudoers.d/90-cloud-init-users" "$BK/90-cloud-init-users.moved"
  state_set old_locked yes
  visudo -c >/dev/null 2>&1 && pass "sudoers valid" || { echo "FAIL sudoers invalid, rolling back"; do_revert; echo "RESULT $STEP FINALIZE FAIL sudoers rolled_back"; exit 1; }
  progress 4/5 "safety net + restart ssh"
  state_set phase FINALIZED_PENDING
  schedule_revert || { echo "FAIL could not schedule auto-revert, rolling back"; do_revert; echo "RESULT $STEP FINALIZE FAIL timer rolled_back"; exit 1; }
  pass "auto-revert in $((REVERT_SECONDS/60)) min unless CONFIRM"
  reload_ssh || { echo "FAIL ssh restart, rolling back"; do_revert; echo "RESULT $STEP FINALIZE FAIL restart rolled_back"; exit 1; }
  progress 5/5 "verify listeners"
  if listening "$PORT" && ! listening 22; then pass "ssh only on $PORT"
  else echo "FAIL listener check, rolling back"; systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1; do_revert; echo "RESULT $STEP FINALIZE FAIL listen rolled_back"; exit 1; fi
  DEADLINE="$(TZ=Asia/Jakarta date -d "+$((REVERT_SECONDS/60)) min" '+%H:%M WIB' 2>/dev/null || echo '10 min')"
  summary "PASS (waiting for CONFIRM)" "port 22 closed, $OLD_USER locked" "auto-revert at $DEADLINE if not confirmed" "NEXT open a NEW terminal: ssh -p $PORT $USERNAME@$EXPECT_IP and run CONFIRM there"
  echo "RESULT $STEP FINALIZE PASS port=$PORT old_user_locked=$OLD_USER revert_at=$DEADLINE"; exit 0 ;;

REVERT)
  [ "$AUTO" = "1" ] || check_host
  if do_revert; then echo "RESULT $STEP REVERT PASS phase=$(state_get phase)"; exit 0; fi
  echo "RESULT $STEP REVERT FAIL"; exit 1 ;;
esac
