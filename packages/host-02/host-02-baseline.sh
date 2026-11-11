#!/usr/bin/env bash
# Nostek Panel - HOST-02: server baseline (WIB, updates, swap, firewall, fail2ban, Docker Engine, restic backup).
# Modes: PREFLIGHT (read-only) | APPLY | CONFIRM | BACKUP-SETUP | BACKUP-TEST | VERIFY (read-only + test items) | REVERT
# Usage: sudo bash host-02-baseline.sh MODE --ack HOST-02 --ssh-port N --expect-ip ADDR --expect-host NAME
#        BACKUP-SETUP also: --s3-endpoint URL --s3-bucket NAME --s3-prefix PATH --stdin
#        (stdin lines: 1 access key id, 2 secret access key, 3 backup password) - never in argv.
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="HOST-02"; VERSION="1"
R="${HOST02_TEST_ROOT:-}"
STATE_DIR="$R/var/lib/nostek/host-02"
BACKUP_ROOT="$R/var/backups/nostek/host-02"
BK_ETC="$R/etc/nostek/backup"
TIMER_UNIT="nostek-host02-revert"
REVERT_SECONDS=600
DOCKER_FPR="9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l
APT_OPTS=(-q -y -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

MODE="${1:-}"; shift || true
ACK=""; SSH_PORT=""; EXPECT_IP=""; EXPECT_HOST=""; S3_ENDPOINT=""; S3_BUCKET=""; S3_PREFIX=""; USE_STDIN=0; AUTO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --ssh-port) SSH_PORT="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --expect-host) EXPECT_HOST="${2:-}"; shift 2 ;;
    --s3-endpoint) S3_ENDPOINT="${2:-}"; shift 2 ;;
    --s3-bucket) S3_BUCKET="${2:-}"; shift 2 ;;
    --s3-prefix) S3_PREFIX="${2:-}"; shift 2 ;;
    --stdin) USE_STDIN=1; shift ;;
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
  case "$MODE" in PREFLIGHT|APPLY|CONFIRM|BACKUP-SETUP|BACKUP-TEST|VERIFY|REVERT) ;; *) refuse "mode must be PREFLIGHT|APPLY|CONFIRM|BACKUP-SETUP|BACKUP-TEST|VERIFY|REVERT" ;; esac
  [ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
  [ "$(id -u)" = "0" ] || refuse "run with sudo"
  if [ "$AUTO" = "0" ] && [ -z "${SUDO_USER:-}" ]; then refuse "no root shell: run from your normal user with sudo"; fi
  [[ "$SSH_PORT" =~ ^[0-9]+$ ]] && [ "$SSH_PORT" -ge 1 ] && [ "$SSH_PORT" -le 65535 ] || refuse "--ssh-port must be the current SSH port"
  [ -n "$EXPECT_IP" ] && [ -n "$EXPECT_HOST" ] || refuse "--expect-ip and --expect-host are required"
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
ssh_listening() { ss -ltnH "sport = :$SSH_PORT" 2>/dev/null | grep -q .; }
ufw_active() { ufw status 2>/dev/null | grep -q "Status: active"; }

# ---------- PREFLIGHT ----------
preflight() {
  progress 1/8 "host identity"; check_host
  progress 2/8 "operating system"
  local id ver; id="$(. "$R/etc/os-release" 2>/dev/null; echo "${ID:-}")"; ver="$(. "$R/etc/os-release" 2>/dev/null; echo "${VERSION_ID:-}")"
  if [ "$id" = ubuntu ] && { [ "$ver" = 24.04 ] || [ "$ver" = 22.04 ]; }; then pass "os ubuntu $ver"; else fail "os must be Ubuntu 22.04/24.04 (found ${id:-?} ${ver:-?})"; fi
  progress 3/8 "ssh port $SSH_PORT"
  if ssh_listening; then pass "ssh listening on $SSH_PORT"; else fail "ssh is not listening on $SSH_PORT (wrong --ssh-port?)"; fi
  progress 4/8 "HOST-01 state"
  if [ "$(grep -E '^phase=' "$R/var/lib/nostek/host-01/state" 2>/dev/null | tail -n1 | cut -d= -f2)" = DONE ]; then pass "HOST-01 done"; else fail "HOST-01 is not DONE on this host"; fi
  progress 5/8 "resources"
  local mem disk; mem="$(awk '/MemTotal/{print int($2/1024)}' "$R/proc/meminfo" 2>/dev/null)"; disk="$(df -Pm "${R:-/}" 2>/dev/null | awk 'NR==2{print $4}')"
  info "ram_mb=${mem:-?} disk_free_mb=${disk:-?} swap=$(swapon --show=NAME --noheadings 2>/dev/null | head -n1 | tr -d ' ')"
  if [ "${disk:-0}" -ge 5000 ]; then pass "disk free ${disk} MB"; else fail "need at least 5000 MB free disk (found ${disk:-?})"; fi
  progress 6/8 "network to package sources"
  if curl -fsS --max-time 10 -o /dev/null https://download.docker.com/linux/ubuntu/gpg; then pass "download.docker.com reachable"; else fail "cannot reach download.docker.com"; fi
  progress 7/8 "firewall / docker / backup now"
  info "ufw=$(ufw_active && echo active || echo inactive) docker=$(command -v docker >/dev/null 2>&1 && echo present || echo absent) restic=$(command -v restic >/dev/null 2>&1 && echo present || echo absent) tz=$(timedatectl show -p Timezone --value 2>/dev/null)"
  progress 8/8 "previous runs"
  info "phase=$(state_get phase)"
  systemctl is-active "$TIMER_UNIT.timer" >/dev/null 2>&1 && warn "firewall revert timer is running"
  return 0
}

# ---------- APPLY pieces ----------
apt_run() { apt-get "${APT_OPTS[@]}" "$@" < /dev/null > "$STATE_DIR/apt.log" 2>&1; }

set_timezone() {
  timedatectl set-timezone Asia/Jakarta && pass "timezone Asia/Jakarta (WIB)" || return 1
}
do_updates() {
  apt_run update || return 1
  apt_run upgrade || return 1
  apt_run install unattended-upgrades fail2ban restic ca-certificates curl gnupg tmux || return 1
  printf 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n' > "$R/etc/apt/apt.conf.d/20auto-upgrades"
  pass "packages upgraded; unattended security upgrades on"
}
ensure_swap() {
  if [ -n "$(swapon --show=NAME --noheadings 2>/dev/null)" ]; then pass "swap already present"; return 0; fi
  fallocate -l 2G "$R/swapfile" && chmod 600 "$R/swapfile" && mkswap "$R/swapfile" >/dev/null && swapon "$R/swapfile" || return 1
  grep -q '^/swapfile ' "$R/etc/fstab" 2>/dev/null || echo '/swapfile none swap sw 0 0' >> "$R/etc/fstab"
  pass "swap 2G added"
}
install_docker() {
  if command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1; then pass "docker already installed"; else
    install -d -m 0755 "$R/etc/apt/keyrings"
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o "$STATE_DIR/docker.asc" || return 1
    local fpr; fpr="$(gpg --show-keys --with-colons "$STATE_DIR/docker.asc" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')"
    [ "$fpr" = "$DOCKER_FPR" ] || { echo "FAIL docker key fingerprint mismatch (${fpr:-none})"; return 1; }
    install -m 0644 "$STATE_DIR/docker.asc" "$R/etc/apt/keyrings/docker.asc"
    local codename arch; codename="$(. "$R/etc/os-release"; echo "${VERSION_CODENAME:-}")"; arch="$(dpkg --print-architecture)"
    echo "deb [arch=$arch signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $codename stable" > "$R/etc/apt/sources.list.d/docker.list"
    apt_run update || return 1
    apt_run install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin || return 1
    pass "docker engine installed (key fingerprint verified)"
  fi
  install -d -m 0755 "$R/etc/docker"
  if [ ! -f "$R/etc/docker/daemon.json" ]; then
    printf '{\n  "log-driver": "local",\n  "log-opts": { "max-size": "20m", "max-file": "5" },\n  "live-restore": true\n}\n' > "$R/etc/docker/daemon.json"
    systemctl restart docker || return 1
  fi
  systemctl enable --now docker >/dev/null 2>&1 || return 1
  pass "docker daemon running (log limits, live-restore)"
}
setup_fail2ban() {
  printf '[DEFAULT]\nbantime = 1h\nfindtime = 10m\nmaxretry = 5\nbackend = systemd\n\n[sshd]\nenabled = true\nport = %s\n' "$SSH_PORT" > "$R/etc/fail2ban/jail.d/nostek.local"
  systemctl enable --now fail2ban >/dev/null 2>&1 && systemctl restart fail2ban || return 1
  pass "fail2ban sshd jail on port $SSH_PORT"
}
setup_firewall() {
  ufw default deny incoming >/dev/null && ufw default allow outgoing >/dev/null || return 1
  ufw allow "$SSH_PORT/tcp" comment "ssh (nostek)" >/dev/null || return 1
  ufw allow 80/tcp comment "http (nostek)" >/dev/null && ufw allow 443/tcp comment "https (nostek)" >/dev/null || return 1
  ufw show added 2>/dev/null | grep -q -- "allow $SSH_PORT/tcp" || { echo "FAIL ssh rule not staged"; return 1; }
  systemd-run --unit="$TIMER_UNIT" --on-active="$REVERT_SECONDS" /usr/sbin/ufw --force disable >/dev/null 2>&1 || { echo "FAIL cannot schedule firewall auto-revert"; return 1; }
  ufw --force enable >/dev/null || return 1
  ufw_active && pass "ufw active: allow $SSH_PORT, 80, 443; auto-disable in $((REVERT_SECONDS/60)) min unless CONFIRM"
}

# ---------- backup ----------
write_backup_runner() {
  install -d -m 0755 "$R/usr/local/sbin"
  cat > "$R/usr/local/sbin/nostek-backup" <<'RUNNER'
#!/usr/bin/env bash
# Nostek Panel nightly backup (restic). Managed by HOST-02.
set -u
# shellcheck disable=SC1090
. "${NOSTEK_BACKUP_ENV:-/etc/nostek/backup/restic.env}"
export RESTIC_REPOSITORY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY RESTIC_PASSWORD
PATHS=(/etc /home /root /srv /var/lib/nostek /var/backups/nostek)
ok=0
restic backup --tag nightly --one-file-system "${PATHS[@]}" --exclude /var/backups/nostek/restore-test || ok=1
restic forget --tag nightly --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune || ok=1
[ "$(date +%u)" = 7 ] && { restic check || ok=1; }
echo "nostek-backup result=$([ $ok = 0 ] && echo PASS || echo FAIL) time=$(TZ=Asia/Jakarta date '+%Y-%m-%d %H:%M WIB')"
exit $ok
RUNNER
  chmod 0755 "$R/usr/local/sbin/nostek-backup"
  printf '[Unit]\nDescription=Nostek Panel nightly backup\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=oneshot\nExecStart=/usr/local/sbin/nostek-backup\nNice=10\nIOSchedulingClass=idle\n' > "$R/etc/systemd/system/nostek-backup.service"
  printf '[Unit]\nDescription=Nostek Panel nightly backup (03:30 WIB)\n\n[Timer]\nOnCalendar=*-*-* 03:30:00 Asia/Jakarta\nRandomizedDelaySec=10m\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n' > "$R/etc/systemd/system/nostek-backup.timer"
}
load_backup_env() {
  [ -f "$BK_ETC/restic.env" ] || return 1
  # shellcheck disable=SC1091
  . "$BK_ETC/restic.env"; export RESTIC_REPOSITORY AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY RESTIC_PASSWORD
}

snap_ids() { restic snapshots --tag "$1" --json 2>/dev/null | grep -o '"id":"[0-9a-f]\{64\}"' | cut -d'"' -f4; }

# ---------- main ----------
check_invocation
mkdir -p "$STATE_DIR"
info "$STEP v$VERSION mode=$MODE time=$(now_wib)"

case "$MODE" in
PREFLIGHT)
  preflight
  if [ "$FAILS" -eq 0 ]; then summary PASS "nothing changed" "NEXT APPLY (run inside tmux)"; echo "RESULT $STEP PREFLIGHT PASS"; exit 0; fi
  summary FAIL "$FAILS check(s) failed, nothing changed"; echo "RESULT $STEP PREFLIGHT FAIL fails=$FAILS"; exit 1 ;;

APPLY)
  preflight; [ "$FAILS" -eq 0 ] || die "preflight failed, nothing changed"
  case "$(state_get phase)" in ""|REVERTED) ;; *) refuse "already applied (phase=$(state_get phase)); use VERIFY or REVERT" ;; esac
  TS="$(date -u +%Y%m%dT%H%M%SZ)"; BK="$BACKUP_ROOT/$TS"; mkdir -p "$BK"
  for f in etc/fstab etc/ufw/user.rules etc/ufw/user6.rules etc/default/ufw etc/docker/daemon.json etc/apt/apt.conf.d/20auto-upgrades; do [ -f "$R/$f" ] && { mkdir -p "$BK/$(dirname "$f")"; cp -a "$R/$f" "$BK/$f"; }; done
  state_set backup "$BK"; pass "config backup at $BK"
  progress 1/6 "timezone"; set_timezone || die "timezone"
  progress 2/6 "system updates (can take several minutes)"; do_updates || die "apt (see $STATE_DIR/apt.log)"
  progress 3/6 "swap"; ensure_swap || die "swap"
  progress 4/6 "docker engine"; install_docker || die "docker"
  progress 5/6 "fail2ban"; setup_fail2ban || die "fail2ban"
  progress 6/6 "firewall (lock-out safe)"
  state_set phase FIREWALL_PENDING
  if ! setup_firewall; then ufw --force disable >/dev/null 2>&1; systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1; state_set phase BASE_DONE; die "firewall (disabled again, base steps kept)"; fi
  ssh_listening || warn "ssh listener not visible after firewall enable"
  DEADLINE="$(TZ=Asia/Jakarta date -d "+$((REVERT_SECONDS/60)) min" '+%H:%M WIB' 2>/dev/null)"
  summary "PASS (waiting for CONFIRM)" "WIB time, updates, swap, docker, fail2ban done" "firewall on; it switches itself off at $DEADLINE unless CONFIRM" "NEXT open a NEW terminal (ssh on port $SSH_PORT) and run CONFIRM"
  echo "RESULT $STEP APPLY PASS docker=$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ,) ufw=active revert_at=$DEADLINE"; exit 0 ;;

CONFIRM)
  check_host
  [ "$(state_get phase)" = FIREWALL_PENDING ] || refuse "nothing to confirm (phase=$(state_get phase))"
  ss -tnH state established "( sport = :$SSH_PORT )" 2>/dev/null | grep -q . || refuse "no SSH session on port $SSH_PORT; open a new login first"
  ufw_active || die "firewall is not active (auto-revert already ran?) — run APPLY again"
  systemctl stop "$TIMER_UNIT.timer" "$TIMER_UNIT.service" >/dev/null 2>&1 || true
  systemctl is-active "$TIMER_UNIT.timer" >/dev/null 2>&1 && die "revert timer still active"
  state_set phase BASE_DONE
  summary PASS "firewall kept, auto-revert cancelled" "NEXT BACKUP-SETUP"
  echo "RESULT $STEP CONFIRM PASS phase=BASE_DONE"; exit 0 ;;

BACKUP-SETUP)
  check_host
  case "$(state_get phase)" in BASE_DONE|BACKUP_READY) ;; *) refuse "run APPLY + CONFIRM first (phase=$(state_get phase))" ;; esac
  [[ "$S3_ENDPOINT" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || refuse "--s3-endpoint must look like https://host"
  [[ "$S3_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || refuse "--s3-bucket is not a valid bucket name"
  [[ "$S3_PREFIX" =~ ^[A-Za-z0-9._-]+$ ]] || refuse "--s3-prefix must be a simple name"
  [ "$USE_STDIN" = 1 ] || refuse "keys and backup password come via --stdin"
  IFS= read -r K1 || true; IFS= read -r K2 || true; IFS= read -r K3 || true
  { [ -n "$K1" ] && [ -n "$K2" ]; } || { K1=""; K2=""; K3=""; refuse "missing access key id or secret"; }
  [ "${#K3}" -ge 16 ] || { K1=""; K2=""; K3=""; refuse "backup password must be at least 16 characters"; }
  install -d -m 0700 "$BK_ETC"
  [ -f "$BK_ETC/restic.env" ] && mv "$BK_ETC/restic.env" "$BK_ETC/restic.env.old-$(date -u +%Y%m%dT%H%M%SZ)"
  ( umask 077; { echo "RESTIC_REPOSITORY='s3:$S3_ENDPOINT/$S3_BUCKET/$S3_PREFIX'"; printf "AWS_ACCESS_KEY_ID='%s'\n" "$K1"; printf "AWS_SECRET_ACCESS_KEY='%s'\n" "$K2"; printf "RESTIC_PASSWORD='%s'\n" "$K3"; } > "$BK_ETC/restic.env" )
  K1=""; K2=""; K3=""
  chmod 600 "$BK_ETC/restic.env"; pass "credentials stored root-only (values not shown)"
  load_backup_env || die "env"
  progress 1/3 "repository"
  if restic cat config >/dev/null 2>&1; then pass "repository exists"; else restic init >/dev/null 2>&1 || die "restic init failed (check endpoint, bucket, keys)"; pass "repository created"; fi
  progress 2/3 "nightly timer"
  write_backup_runner; systemctl daemon-reload && systemctl enable --now nostek-backup.timer >/dev/null 2>&1 || die "timer"
  pass "nightly backup 03:30 WIB (keep 7 daily, 4 weekly, 6 monthly)"
  progress 3/3 "state"; state_set phase BACKUP_READY
  summary PASS "repository s3:$S3_ENDPOINT/$S3_BUCKET/$S3_PREFIX ready" "NEXT BACKUP-TEST (backup + restore proof)"
  echo "RESULT $STEP BACKUP-SETUP PASS repo_bucket=$S3_BUCKET prefix=$S3_PREFIX"; exit 0 ;;

BACKUP-TEST)
  check_host
  case "$(state_get phase)" in BACKUP_READY|DONE) ;; *) refuse "run BACKUP-SETUP first" ;; esac
  load_backup_env || die "no backup config"
  T="$R/var/backups/nostek/restore-test"; mkdir -p "$T/src"
  head -c 65536 /dev/urandom > "$T/src/probe.bin"; SUM="$(sha256sum "$T/src/probe.bin" | awk '{print $1}')"
  progress 1/4 "test backup"; restic backup --tag host02-test "$T/src" >/dev/null 2>&1 || die "test backup"
  progress 2/4 "full backup"; NOSTEK_BACKUP_ENV="$BK_ETC/restic.env" "$R/usr/local/sbin/nostek-backup" > "$STATE_DIR/backup-first.log" 2>&1 || die "first full backup (see $STATE_DIR/backup-first.log)"
  progress 3/4 "restore test"
  restic restore latest --tag host02-test --target "$T/restore" >/dev/null 2>&1 || die "restore"
  GOT="$(sha256sum "$T/restore$T/src/probe.bin" 2>/dev/null | awk '{print $1}')"
  [ "$GOT" = "$SUM" ] || die "restored file differs"
  pass "restore matches (sha256)"
  progress 4/4 "clean test items"
  # forget needs snapshot ids (a tag alone removes nothing)
  mapfile -t TIDS < <(snap_ids host02-test)
  [ "${#TIDS[@]}" -gt 0 ] && restic forget --prune "${TIDS[@]}" >/dev/null 2>&1
  rm -rf "$T"
  LEFT=$(( $(snap_ids host02-test | wc -l) + $([ -e "$T" ] && echo 1 || echo 0) ))
  SNAPS="$(snap_ids nightly | wc -l)"
  state_set phase DONE
  summary PASS "full backup done, restore proven" "BACKUP-FIRST satisfied: server may be used" "NEXT VERIFY"
  echo "RESULT $STEP BACKUP-TEST PASS restore=ok nightly_snapshots=$SNAPS sisa_uji=$LEFT"; exit 0 ;;

VERIFY)
  check_host
  progress 1/6 "timezone"; [ "$(timedatectl show -p Timezone --value 2>/dev/null)" = Asia/Jakarta ] && pass "WIB" || fail "timezone not Asia/Jakarta"
  progress 2/6 "firewall"; if ufw_active && ufw status 2>/dev/null | grep -q "^$SSH_PORT/tcp .*ALLOW"; then pass "ufw active, ssh allowed"; else fail "ufw not active or ssh rule missing"; fi
  progress 3/6 "fail2ban"; fail2ban-client status sshd >/dev/null 2>&1 && pass "fail2ban sshd jail" || fail "fail2ban sshd jail missing"
  progress 4/6 "docker"
  if docker run --rm hello-world >/dev/null 2>&1; then pass "docker runs containers"; else fail "docker test container failed"; fi
  docker image rm hello-world >/dev/null 2>&1
  LEFT="$(docker image ls -q hello-world 2>/dev/null | wc -l)"
  progress 5/6 "backup timer"; systemctl is-enabled nostek-backup.timer >/dev/null 2>&1 && pass "nightly backup timer enabled" || fail "backup timer not enabled"
  progress 6/6 "phase"; [ "$(state_get phase)" = DONE ] && pass "phase DONE" || fail "phase $(state_get phase), expected DONE"
  [ -f "$R/var/run/reboot-required" ] && warn "a reboot is pending after updates (plan it; not required now)"
  if [ "$FAILS" -eq 0 ]; then summary PASS "server baseline complete" "NEXT PANEL-01"; echo "RESULT $STEP VERIFY PASS sisa_uji=$LEFT"; exit 0; fi
  summary FAIL "$FAILS check(s) failed"; echo "RESULT $STEP VERIFY FAIL fails=$FAILS sisa_uji=$LEFT"; exit 1 ;;

REVERT)
  [ "$AUTO" = 1 ] || check_host
  ufw --force disable >/dev/null 2>&1
  systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1 || true
  state_set phase REVERTED
  summary PASS "firewall disabled; packages, Docker and backup config kept (harmless)" "NEXT fix the cause, then APPLY again"
  echo "RESULT $STEP REVERT PASS"; exit 0 ;;
esac
