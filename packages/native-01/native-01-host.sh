#!/usr/bin/env bash
# Nostek Panel - NATIVE-01: the optional native website engine on the host (owner decision NATIVE-1).
# Installs one host Nginx (only on the edge network's gateway address, port 8088), PHP-FPM 8.1-8.5 from the
# ondrej/php PPA (the distribution services stay off: every native website runs its own PHP-FPM as its own user),
# one host MariaDB 10.11 on its unix socket only (no TCP), and WP-CLI. The agent learns about it from
# /etc/nostek-agent/native.json; Docker stays the default engine and every Docker website keeps running.
# Modes: PREFLIGHT (read-only) | APPLY (install or update) | VERIFY (read-only) | REVERT
# Usage: sudo bash native-01-host.sh MODE --ack NATIVE-01 --expect-ip ADDR --expect-host NAME
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="NATIVE-01"
PHP_VERSIONS="8.1 8.2 8.3 8.4 8.5"
PHP_EXT="cli fpm mysql curl gd intl mbstring xml zip bcmath"
PHP_EXT_OPTIONAL="opcache imagick"   # opcache is built into PHP 8.5; imagick may come later for a new PHP
WPCLI_VERSION="2.12.0"
WPCLI_SHA512="be928f6b8ca1e8dfb9d2f4b75a13aa4aee0896f8a9a0a1c45cd5d2c98605e6172e6d014dda2e27f88c98befc16c040cbb2bd1bfa121510ea5cdf5f6a30fe8832"
PORT=8088
VARLIB="/var/lib/nostek"
STATE_DIR="$VARLIB/native-01"
SITE01_STATE="$VARLIB/site-01/state"
BACKUP_ROOT="/var/backups/nostek/native-01"
NATIVE_JSON="/etc/nostek-agent/native.json"
NGX="/etc/nginx"
EDGE_DYNAMIC="/srv/nostek/edge/dynamic"

MODE="${1:-}"; shift || true
ACK=""; EXPECT_IP=""; EXPECT_HOST=""; AUTO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --ack) ACK="${2:-}"; shift 2 ;;
    --expect-ip) EXPECT_IP="${2:-}"; shift 2 ;;
    --expect-host) EXPECT_HOST="${2:-}"; shift 2 ;;
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
  case "$MODE" in PREFLIGHT|APPLY|VERIFY|REVERT) ;; *) refuse "mode must be PREFLIGHT|APPLY|VERIFY|REVERT" ;; esac
  [ "$ACK" = "$STEP" ] || refuse "--ack $STEP is required"
  [ "$(id -u)" = "0" ] || refuse "run with sudo"
  if [ "$AUTO" = "0" ] && [ -z "${SUDO_USER:-}" ]; then refuse "no root shell: run from your normal user with sudo"; fi
  [ -n "$EXPECT_IP" ] && [ -n "$EXPECT_HOST" ] || refuse "--expect-ip and --expect-host are required"
  [[ "$EXPECT_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || refuse "--expect-ip must be an IPv4 address"
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

# the edge network's gateway (the host's address on it) and subnet: host Nginx listens there only
edge_gateway() { docker network inspect nostek-edge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null; }
edge_subnet() { docker network inspect nostek-edge -f '{{(index .IPAM.Config 0).Subnet}}' 2>/dev/null; }
native_sites() { grep -l '"engine": *"native"' /srv/nostek/sites/*/site.json 2>/dev/null | wc -l; }
php_packages() {
  local v e out=""
  for v in $PHP_VERSIONS; do
    for e in $PHP_EXT; do out="$out php$v-$e"; done
    for e in $PHP_EXT_OPTIONAL; do apt-cache show "php$v-$e" >/dev/null 2>&1 && out="$out php$v-$e"; done
  done
  echo "$out"
}

# ---------- PREFLIGHT ----------
preflight() {
  progress 1/5 "host identity"; check_host
  progress 2/5 "operating system"
  # shellcheck disable=SC1091
  if [ -r /etc/os-release ] && . /etc/os-release && [ "${VERSION_CODENAME:-}" = noble ]; then pass "Ubuntu 24.04 (noble)"; else fail "needs Ubuntu 24.04 (found ${PRETTY_NAME:-unknown})"; fi
  command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] && pass "systemd running" || fail "systemd is not running"
  progress 3/5 "SITE-01 and the edge"
  if grep -qx 'phase=APPLIED' "$SITE01_STATE" 2>/dev/null; then pass "SITE-01 applied (agent installed)"; else fail "SITE-01 is not applied: install the agent first"; fi
  local gw sn; gw="$(edge_gateway)"; sn="$(edge_subnet)"
  if [[ "$gw" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && [ -n "$sn" ]; then pass "edge network $sn, host address $gw"; else fail "network nostek-edge has no IPv4 gateway (PANEL-01 core not running)"; fi
  progress 4/5 "ports and services already there"
  if ss -Htln 2>/dev/null | awk '{print $4}' | grep -qE "(^|:)$PORT\$"; then
    if [ "$(state_get phase)" = APPLIED ]; then pass "port $PORT used by our Nginx"; else fail "port $PORT is already used by something else"; fi
  else pass "port $PORT free"; fi
  if ss -Htln 2>/dev/null | awk '{print $4}' | grep -qE '(^|:)3306$'; then fail "something listens on TCP 3306: the host MariaDB must be socket-only"; else pass "no database on TCP 3306"; fi
  if dpkg -s apache2 >/dev/null 2>&1; then fail "apache2 is installed on the host: remove it first (sites use Traefik on 80/443)"; else pass "no host Apache"; fi
  progress 5/5 "resources"
  local disk mem; disk="$(df -Pm / 2>/dev/null | awk 'NR==2{print $4}')"
  if [ "${disk:-0}" -ge 3000 ]; then pass "disk free ${disk} MB"; else fail "need at least 3000 MB free disk for the packages (found ${disk:-?})"; fi
  mem="$(awk '/MemAvailable/{print int($2/1024)}' /proc/meminfo 2>/dev/null)"
  if [ "${mem:-0}" -ge 600 ]; then pass "memory available ${mem} MB"; else warn "memory available ${mem:-?} MB: MariaDB needs about 150 MB"; fi
  info "phase=$(state_get phase) native_sites=$(native_sites)"
  return 0
}

write_nginx() { # $1 gateway
  mkdir -p "$NGX/nostek" "$NGX/conf.d"
  rm -f "$NGX/sites-enabled/default"
  cat > "$NGX/conf.d/00-nostek.conf" <<EOF
# Installed by Nostek Panel NATIVE-01. Native websites are only reached through the edge (Traefik), which sets
# X-Forwarded-Proto; nothing else can reach this address (listen address and firewall).
map \$http_x_forwarded_proto \$nostek_https { default ""; https on; }
set_real_ip_from $(edge_subnet);
real_ip_header X-Forwarded-For;
server_tokens off;
server {
    listen $1:$PORT default_server;
    server_name _;
    return 444;
}
include $NGX/nostek/*.conf;
EOF
  # Nginx starts after Docker, which puts the edge address on the host
  mkdir -p /etc/systemd/system/nginx.service.d
  cat > /etc/systemd/system/nginx.service.d/nostek.conf <<'EOF'
# Installed by Nostek Panel NATIVE-01: listen on the edge network's address, which exists once Docker runs.
[Unit]
After=docker.service
Wants=docker.service
EOF
  echo 'net.ipv4.ip_nonlocal_bind = 1' > /etc/sysctl.d/60-nostek-native.conf
  sysctl -q -p /etc/sysctl.d/60-nostek-native.conf
}

write_mariadb() {
  cat > /etc/mysql/mariadb.conf.d/60-nostek.cnf <<'EOF'
# Installed by Nostek Panel NATIVE-01: native websites reach MariaDB on its unix socket only.
[mysqld]
skip-networking = 1
bind-address = 127.0.0.1
local-infile = 0
innodb_buffer_pool_size = 128M
max_connections = 200
EOF
}

write_native_json() { # $1 gateway
  local v list=""
  for v in $PHP_VERSIONS; do list="$list\"$v\","; done
  mkdir -p "$(dirname "$NATIVE_JSON")"
  cat > "$NATIVE_JSON.new" <<EOF
{
  "gateway": "$1",
  "port": $PORT,
  "php": [${list%,}],
  "mariadb_socket": "/run/mysqld/mysqld.sock",
  "wp_cli": "/usr/local/bin/wp"
}
EOF
  chmod 0644 "$NATIVE_JSON.new" && mv "$NATIVE_JSON.new" "$NATIVE_JSON"
}

apply() {
  preflight
  [ "$FAILS" = 0 ] || die "preflight failed ($FAILS); nothing changed"
  local gw sn v; gw="$(edge_gateway)"; sn="$(edge_subnet)"
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)-apply"; mkdir -p "$BK"
  [ -f "$NATIVE_JSON" ] && cp -p "$NATIVE_JSON" "$BK/"
  state_set phase APPLYING
  export DEBIAN_FRONTEND=noninteractive
  progress A1/6 "PHP repository (ondrej/php PPA)"
  if ! grep -rqs 'ppa.launchpadcontent.net/ondrej/php' /etc/apt/sources.list.d/; then
    apt-get update -qq && apt-get install -y -qq software-properties-common >/dev/null || die "cannot install software-properties-common"
    add-apt-repository -y ppa:ondrej/php >/dev/null 2>&1 || die "cannot add the ondrej/php PPA"
  fi
  apt-get update -qq || die "apt-get update failed"
  pass "ondrej/php PPA present"
  progress A2/6 "packages: Nginx, PHP-FPM $PHP_VERSIONS, MariaDB"
  # no service may start while installing: Nginx would try port 80, which Traefik holds
  printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod 0755 /usr/sbin/policy-rc.d
  # shellcheck disable=SC2046
  apt-get install -y -qq --no-install-recommends nginx mariadb-server mariadb-client $(php_packages) >/dev/null; local rc=$?
  rm -f /usr/sbin/policy-rc.d
  [ $rc = 0 ] || die "package install failed (see /var/log/apt/term.log)"
  pass "packages installed"
  # every native website runs its own PHP-FPM as its own user: the distribution pools stay off
  for v in $PHP_VERSIONS; do systemctl disable -q --now "php$v-fpm.service" >/dev/null 2>&1; done
  pass "distribution PHP-FPM services off"
  progress A3/6 "WP-CLI $WPCLI_VERSION"
  if [ "$(sha512sum /usr/local/bin/wp 2>/dev/null | cut -d' ' -f1)" != "$WPCLI_SHA512" ]; then
    curl -fsSL -o /usr/local/bin/wp.new "https://github.com/wp-cli/wp-cli/releases/download/v$WPCLI_VERSION/wp-cli-$WPCLI_VERSION.phar" || die "cannot download WP-CLI"
    [ "$(sha512sum /usr/local/bin/wp.new | cut -d' ' -f1)" = "$WPCLI_SHA512" ] || { rm -f /usr/local/bin/wp.new; die "WP-CLI checksum does not match"; }
    chmod 0755 /usr/local/bin/wp.new && mv /usr/local/bin/wp.new /usr/local/bin/wp
  fi
  pass "WP-CLI $WPCLI_VERSION (sha512 checked)"
  progress A4/6 "MariaDB on its socket only"
  write_mariadb
  systemctl enable -q mariadb.service && systemctl restart mariadb.service || die "MariaDB did not start"
  pass "MariaDB running, no TCP"
  progress A5/6 "Nginx on $gw:$PORT"
  write_nginx "$gw"
  systemctl daemon-reload
  nginx -t -q 2>"$BK/nginx-t.log" || die "nginx configuration invalid ($(tail -n1 "$BK/nginx-t.log"))"
  systemctl enable -q nginx.service && systemctl restart nginx.service || die "Nginx did not start"
  if command -v ufw >/dev/null 2>&1; then
    ufw allow proto tcp from "$sn" to "$gw" port "$PORT" comment 'nostek native: edge to host nginx' >/dev/null || die "cannot add the firewall rule"
    pass "firewall: only $sn reaches $gw:$PORT"
  fi
  mkdir -p "$EDGE_DYNAMIC"; chmod 0755 "$EDGE_DYNAMIC"
  pass "Nginx running on $gw:$PORT"
  progress A6/6 "tell the agent"
  write_native_json "$gw"
  if systemctl is-active -q nostek-agent.service; then systemctl restart nostek-agent.service; fi
  pass "agent sees the native engine ($NATIVE_JSON)"
  state_set phase APPLIED; state_set gateway "$gw"
  summary PASS "native engine ready: Nginx $gw:$PORT, PHP-FPM $PHP_VERSIONS, MariaDB (socket), WP-CLI $WPCLI_VERSION" \
    "Docker websites untouched; Docker stays the default engine" "NEXT: VERIFY, then the panel offers Engine: Native on Add website"
  echo "RESULT $STEP APPLY PASS php=${PHP_VERSIONS// /,} gateway=$gw"; exit 0
}

verify() {
  progress 1/5 "host identity"; check_host
  progress 2/5 "state"
  [ "$(state_get phase)" = APPLIED ] && pass "NATIVE-01 applied" || fail "NATIVE-01 not applied (phase=$(state_get phase))"
  progress 3/5 "packages and services"
  local v gw; gw="$(edge_gateway)"
  for v in $PHP_VERSIONS; do
    [ -x "/usr/sbin/php-fpm$v" ] && pass "php-fpm$v present" || fail "php-fpm$v missing"
    systemctl is-enabled -q "php$v-fpm.service" 2>/dev/null && fail "php$v-fpm.service (distribution pool) is enabled" || true
  done
  [ "$(sha512sum /usr/local/bin/wp 2>/dev/null | cut -d' ' -f1)" = "$WPCLI_SHA512" ] && pass "WP-CLI $WPCLI_VERSION" || fail "WP-CLI missing or changed"
  systemctl is-active -q nginx.service && pass "nginx active" || fail "nginx not active"
  systemctl is-active -q mariadb.service && pass "mariadb active" || fail "mariadb not active"
  progress 4/5 "only the edge reaches them"
  local l; l="$(ss -Htlnp 2>/dev/null | grep nginx | awk '{print $4}' | sort -u | tr '\n' ' ')"
  [ "$l" = "$gw:$PORT " ] && pass "nginx listens only on $gw:$PORT" || fail "nginx listens on: ${l:-nothing} (expected $gw:$PORT only)"
  ss -Htlnp 2>/dev/null | grep -q -E 'mysqld|mariadbd' && fail "MariaDB listens on TCP" || pass "MariaDB socket only"
  [ -S /run/mysqld/mysqld.sock ] && pass "MariaDB socket present" || fail "MariaDB socket missing"
  local code; code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://$EXPECT_IP:$PORT/" 2>/dev/null)"
  [ "$code" = 000 ] && pass "port $PORT closed on the public address" || fail "port $PORT answers on $EXPECT_IP ($code)"
  progress 5/5 "agent"
  grep -q "\"gateway\": \"$gw\"" "$NATIVE_JSON" 2>/dev/null && pass "$NATIVE_JSON names $gw" || fail "$NATIVE_JSON missing or names another gateway"
  local n; n="$(native_sites)"; info "native websites: $n"
  if [ "$FAILS" = 0 ]; then summary PASS "native engine ready" "native websites: $n"; echo "RESULT $STEP VERIFY PASS native_sites=$n sisa_uji=0"; exit 0; fi
  summary FAIL "$FAILS checks failed"; echo "RESULT $STEP VERIFY FAIL fails=$FAILS"; exit 1
}

revert() {
  check_host
  local n; n="$(native_sites)"
  [ "$n" = 0 ] || refuse "$n native website(s) still exist: move or delete them first (nothing changed)"
  BK="$BACKUP_ROOT/$(date +%Y%m%d-%H%M%S)-revert"; mkdir -p "$BK"
  [ -f "$NATIVE_JSON" ] && mv "$NATIVE_JSON" "$BK/"
  systemctl is-active -q nostek-agent.service && systemctl restart nostek-agent.service
  pass "agent no longer offers the native engine"
  systemctl disable -q --now nginx.service mariadb.service >/dev/null 2>&1
  pass "host Nginx and MariaDB stopped (packages and databases kept)"
  if command -v ufw >/dev/null 2>&1; then
    ufw delete allow proto tcp from "$(edge_subnet)" to "$(state_get gateway)" port "$PORT" >/dev/null 2>&1 || true
  fi
  state_set phase REVERTED
  summary PASS "native engine off; Docker websites untouched" "undo: run APPLY again"
  echo "RESULT $STEP REVERT PASS"; exit 0
}

check_invocation
case "$MODE" in
  PREFLIGHT) preflight; if [ "$FAILS" = 0 ]; then summary PASS "ready for APPLY"; echo "RESULT $STEP PREFLIGHT PASS"; exit 0; fi
             summary FAIL "$FAILS checks failed"; echo "RESULT $STEP PREFLIGHT FAIL fails=$FAILS"; exit 1 ;;
  APPLY) apply ;;
  VERIFY) verify ;;
  REVERT) revert ;;
esac
