#!/usr/bin/env bash
# Nostek Panel - INST-01: one-command installer for a fresh Ubuntu 22.04/24.04 or Debian 12 server (amd64 or arm64).
# Checks the server first (system, memory, disk, host name, public address, the panel name's DNS, ports, the
# download sources), then sets the base (time zone, updates, swap, Docker Engine with its signing key checked,
# fail2ban, firewall) and installs the panel (PANEL-01) and its agent (SITE-01) from one release. The release is
# fetched by its commit id (git checks the content) and each install script must match the sha256 in the
# release's own lock file. At the end it prints a one-time link to create the first admin.
# With --restore FILE (BAK-02) it moves a server here instead: the server backup is checked (passphrase, every
# part) after the base is set and before the panel is installed, then the websites and the panel's database come
# back; the admins of the old server sign in as before, so no admin link is printed.
# Modes: PREFLIGHT (read-only) | INSTALL
# Usage: sudo bash nostek-install.sh INSTALL --domain panel.example.com [--preview-zone preview.example.com]
#        [--timezone Area/City] [--release vX.Y.Z | --src-commit SHA] [--ip PUBLIC_IPV4] [--yes]
#        [--restore /root/nostek-server-....npbk [--passphrase-file FILE]]
# Backups (restic to S3) are set up afterwards; the dashboard says "No backup result yet" until then.
# SSH is left as it is (user, port, keys); its port stays open in the firewall.
# Licence: MIT
set -u   # no pipefail: "cmd | grep -q" must not fail on SIGPIPE
umask 022

STEP="INST-01"; INSTALLER_VERSION="2"
MIN_RELEASE="1.32.0"   # the first release whose PANEL-01 accepts this installer's baseline
MIN_RESTORE="1.34.0"   # the first release that can restore a server backup (BAK-02)
MIN_INST02="1.47.0"    # the first release built for Debian 12 and arm64 (INST-02: ClamAV image per architecture)
R="${INST01_TEST_ROOT:-}"
VARLIB="$R/var/lib/nostek"
STATE_DIR="$VARLIB/inst-01"
SRC_ROOT="$R/srv/nostek/src"
LOG="$R/var/log/nostek-install.log"
TIMER_UNIT="nostek-inst01-revert"
REVERT_SECONDS=600
DOCKER_FPR="9DC858229FC7DD38854AE2D88D81803C0EBFCD88"   # the same key signs the Ubuntu and the Debian repository
DISTRO="ubuntu"   # set by the system check: ubuntu | debian (INST-02); picks the Docker repository
ARCH=""           # set by the system check: amd64 | arm64
REPO_URL="https://github.com/thuekx/docker-panel-VPS.git"
if [ -n "$R" ]; then REPO_URL="${INST01_TEST_REPO:-$REPO_URL}"; fi   # test hook: only with a test root
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l
APT_OPTS=(-q -y -o Dpkg::Use-Pty=0 -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

MODE="${1:-}"; shift || true
DOMAIN=""; ZONE=""; TZONE=""; RELEASE=""; COMMIT=""; IP=""; YES=0; RESTORE=""; PASSF=""; PASS_TMP=""
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --preview-zone) ZONE="${2:-}"; shift 2 ;;
    --timezone) TZONE="${2:-}"; shift 2 ;;
    --release) RELEASE="${2:-}"; shift 2 ;;
    --src-commit) COMMIT="${2:-}"; shift 2 ;;
    --ip) IP="${2:-}"; shift 2 ;;
    --yes) YES=1; shift ;;
    --restore) RESTORE="${2:-}"; shift 2 ;;
    --passphrase-file) PASSF="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "REFUSE unknown argument: $1"; exit 2 ;;
  esac
done

FAILS=0
pass() { echo "PASS $*"; }
warn() { echo "WARN $*"; }
fail() { echo "FAIL $*"; FAILS=$((FAILS+1)); }
info() { echo "INFO $*"; }
progress() { echo "PROGRESS $1 $2"; }
refuse() { echo "REFUSE $*"; echo "RESULT $STEP $MODE REFUSE $*"; exit 3; }
die() { echo "FAIL $*"; echo "SUMMARY full log: ${LOG#"$R"}"; echo "RESULT $STEP $MODE FAIL $*"; exit 1; }
state_get() { [ -f "$STATE_DIR/state" ] && grep -E "^$1=" "$STATE_DIR/state" | tail -n1 | cut -d= -f2-; }
state_set() {
  mkdir -p "$STATE_DIR"; touch "$STATE_DIR/state"
  grep -vE "^$1=" "$STATE_DIR/state" > "$STATE_DIR/state.tmp" || true
  echo "$1=$2" >> "$STATE_DIR/state.tmp"; mv "$STATE_DIR/state.tmp" "$STATE_DIR/state"
}
HOSTRE='^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$'
IPRE='^([0-9]{1,3}\.){3}[0-9]{1,3}$'

check_invocation() {
  case "$MODE" in PREFLIGHT|INSTALL) ;; *) refuse "mode must be PREFLIGHT or INSTALL (see --help)" ;; esac
  [ "$(id -u)" = "0" ] || refuse "run as root (sudo bash nostek-install.sh ...)"
  DOMAIN="$(printf '%s' "$DOMAIN" | tr 'A-Z' 'a-z')"
  [[ "$DOMAIN" =~ $HOSTRE ]] || refuse "--domain must be the panel's host name, such as panel.example.com"
  if [ -z "$ZONE" ]; then
    local parent="${DOMAIN#*.}"
    [[ "$parent" == *.* ]] || refuse "--preview-zone is needed (no parent zone in $DOMAIN)"
    ZONE="preview.$parent"
  fi
  ZONE="$(printf '%s' "$ZONE" | tr 'A-Z' 'a-z')"
  [[ "$ZONE" =~ $HOSTRE ]] || refuse "--preview-zone must be a zone such as preview.example.com"
  [ "$ZONE" != "$DOMAIN" ] || refuse "--preview-zone must differ from the panel name"
  if [ -n "$TZONE" ]; then [[ "$TZONE" =~ ^[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}$ ]] || refuse "--timezone must look like Asia/Jakarta"; fi
  if [ -n "$RELEASE" ] && [ -n "$COMMIT" ]; then refuse "give --release or --src-commit, not both"; fi
  if [ -n "$RELEASE" ]; then [[ "$RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || refuse "--release must look like v1.32.0"; fi
  if [ -n "$COMMIT" ]; then [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || refuse "--src-commit must be a full 40-character commit id"; fi
  if [ -n "$IP" ]; then [[ "$IP" =~ $IPRE ]] || refuse "--ip must be an IPv4 address"; fi
  if [ -n "$PASSF" ] && [ -z "$RESTORE" ]; then refuse "--passphrase-file goes with --restore"; fi
  if [ -n "$RESTORE" ]; then
    [[ "$RESTORE" = /* ]] || RESTORE="$PWD/$RESTORE"
    [ -f "$R$RESTORE" ] && [ -r "$R$RESTORE" ] || refuse "--restore: no readable file $RESTORE"
    if [ -n "$PASSF" ]; then
      [[ "$PASSF" = /* ]] || PASSF="$PWD/$PASSF"
      [ -f "$R$PASSF" ] && [ -r "$R$PASSF" ] || refuse "--passphrase-file: no readable file $PASSF"
      PASSF="$R$PASSF"
    fi
  fi
}

# The passphrase is read from its file or asked on the terminal (not shown) and kept in a private file that is
# removed when the installer ends; it never goes on a command line or into the log.
ask_passphrase() {
  [ -z "$RESTORE" ] && return 0
  [ -n "$PASSF" ] && return 0
  { : < /dev/tty; } 2>/dev/null || refuse "no terminal to ask the passphrase: give it with --passphrase-file FILE"
  local d a b; d="$(umask 077; mktemp -d)"; PASS_TMP="$d"; trap 'rm -rf "$PASS_TMP"' EXIT
  printf 'Passphrase of the server backup: ' > /dev/tty; IFS= read -rs a < /dev/tty || a=""; printf '\n' > /dev/tty
  printf 'Again: ' > /dev/tty; IFS= read -rs b < /dev/tty || b=""; printf '\n' > /dev/tty
  [ -n "$a" ] && [ "$a" = "$b" ] || refuse "the two passphrases are not the same"
  (umask 077; printf '%s' "$a" > "$d/pass"); PASSF="$d/pass"
}

# ---------- checks ----------
public_ip() {
  local url v
  for url in https://api.ipify.org https://ifconfig.me/ip; do
    v="$(curl -fsS --max-time 6 "$url" 2>/dev/null | tr -d '[:space:]')"
    [[ "$v" =~ $IPRE ]] && { echo "$v"; return 0; }
  done
  return 1
}
ssh_ports() { # the ports sshd listens on (22 when it cannot be read)
  local p
  p="$(sshd -T 2>/dev/null | awk '$1=="port"{print $2}' | sort -un | tr '\n' ' ')"
  [ -n "${p// /}" ] || p="$(ss -ltnpH 2>/dev/null | awk '/"sshd"/{n=split($4,a,":"); print a[n]}' | sort -un | tr '\n' ' ')"
  [ -n "${p// /}" ] || p="22"
  echo "$p"
}
ver_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; } # $1 >= $2
# the oldest release this server can take: Debian 12 and arm64 need a release built for them
min_release() { if [ "$DISTRO" = debian ] || [ "$ARCH" = arm64 ]; then echo "$MIN_INST02"; else echo "$MIN_RELEASE"; fi; }

preflight() {
  progress 1/8 "system"
  local id ver
  id="$(. "$R/etc/os-release" 2>/dev/null; echo "${ID:-}")"; ver="$(. "$R/etc/os-release" 2>/dev/null; echo "${VERSION_ID:-}")"
  case "$id $ver" in
    "ubuntu 22.04"|"ubuntu 24.04") DISTRO=ubuntu; pass "Ubuntu $ver" ;;
    "debian 12") DISTRO=debian; pass "Debian $ver (websites use the Docker engine; the native engine needs Ubuntu 24.04)" ;;
    *) fail "needs Ubuntu 22.04, Ubuntu 24.04 or Debian 12 (found ${id:-?} ${ver:-?})" ;;
  esac
  ARCH="$(dpkg --print-architecture 2>/dev/null)"
  case "$ARCH" in amd64|arm64) pass "architecture $ARCH" ;; *) fail "needs amd64 or arm64 (found ${ARCH:-?})" ;; esac
  command -v systemctl >/dev/null 2>&1 && pass "systemd" || fail "systemd is needed"
  progress 2/8 "memory and disk"
  local mem disk
  mem="$(awk '/^MemTotal:/{print int($2/1024)}' "$R/proc/meminfo" 2>/dev/null)"
  if [ "${mem:-0}" -ge 1800 ]; then pass "memory ${mem} MB"; elif [ "${mem:-0}" -ge 900 ]; then warn "memory ${mem} MB: works with swap, 2 GB or more is better"; else fail "needs at least 1 GB memory (found ${mem:-?} MB)"; fi
  disk="$(df -Pm "${R:-/}" 2>/dev/null | awk 'NR==2{print $4}')"
  if [ "${disk:-0}" -ge 15000 ]; then pass "disk free ${disk} MB"; else fail "needs at least 15 GB free disk (found ${disk:-?} MB)"; fi
  progress 3/8 "host name"
  HOST="$(hostname 2>/dev/null)"
  if [[ "$HOST" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]{0,62})$ ]]; then pass "host name $HOST"; else fail "the server's host name is not usable (${HOST:-empty}); set one with hostnamectl set-hostname NAME"; fi
  progress 4/8 "public address"
  if [ -z "$IP" ]; then IP="$(public_ip)" || IP=""; fi
  if [ -n "$IP" ]; then pass "public address $IP"; else fail "cannot find the public IPv4 address; give it with --ip"; fi
  progress 5/8 "DNS"
  local a w
  a="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')"
  if [ -n "$IP" ] && printf '%s\n' $a | grep -qxF "$IP"; then pass "$DOMAIN points to $IP"
  else fail "$DOMAIN must have an A record to ${IP:-this server} (found: ${a:-nothing}); the HTTPS certificate needs it"; fi
  w="$(getent ahostsv4 "np-check-$RANDOM.$ZONE" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' ')"
  if [ -n "$IP" ] && printf '%s\n' $w | grep -qxF "$IP"; then pass "*.$ZONE points to $IP (preview addresses)"
  else warn "*.$ZONE does not point here yet (found: ${w:-nothing}): websites without their own DNS get no preview address until it does"; fi
  progress 6/8 "ports"
  local busy
  busy="$(ss -ltnH '( sport = :80 or sport = :443 or sport = :2222 )' 2>/dev/null | wc -l)"
  if [ "$busy" = 0 ]; then pass "ports 80, 443 and 2222 free"; else fail "ports 80/443/2222 are in use by another program (another web server or panel?)"; fi
  SSHP="$(ssh_ports)"; info "SSH listens on: $SSHP(kept open in the firewall)"
  progress 7/8 "download sources"
  local u
  command -v curl >/dev/null 2>&1 || fail "curl is needed: apt-get install -y curl"
  for u in "https://download.docker.com/linux/$DISTRO/gpg" https://github.com https://registry-1.docker.io/v2/; do
    if curl -sS --max-time 10 -o /dev/null "$u" 2>/dev/null; then pass "reachable: ${u#https://}"; else fail "cannot reach ${u#https://}"; fi
  done
  progress 8/8 "earlier installs"
  local p1 h2
  p1="$(grep -E '^phase=' "$VARLIB/panel-01/state" 2>/dev/null | tail -n1 | cut -d= -f2)"
  h2="$(grep -E '^phase=' "$VARLIB/host-02/state" 2>/dev/null | tail -n1 | cut -d= -f2)"
  if [ "$(state_get phase)" = DONE ]; then fail "already installed (version $(state_get release)); update from the panel's update guide"
  elif [ "$p1" = APPLIED ]; then fail "the panel is already installed here (PANEL-01); nothing to do"
  else pass "no earlier panel install"; fi
  [ -n "$h2" ] && info "HOST-02 phase=$h2 (its baseline is kept)"
  if [ -n "$RESTORE" ]; then
    local magic n; magic="$(head -c 6 "$R$RESTORE" 2>/dev/null | tr -d '\n')"
    if [ "$magic" = NPBK1 ] || [ "$magic" = NPBK2 ]; then pass "server backup $RESTORE ($(du -h "$R$RESTORE" 2>/dev/null | cut -f1))"; else fail "$RESTORE is not a Nostek server backup (.npbk)"; fi
    if [ -n "$PASSF" ]; then
      n="$(LC_ALL=C.UTF-8 tr -d '\r\n' < "$PASSF" | wc -m)"
      if [ "$n" -ge 12 ]; then pass "passphrase file ($n characters)"; else fail "the passphrase has fewer than 12 characters"; fi
    fi
    if [ -d "$R/srv/nostek/sites" ] && [ -n "$(ls -A "$R/srv/nostek/sites" 2>/dev/null)" ]; then fail "this server has websites already; a server backup is restored on a fresh server only"; fi
  fi
  if command -v docker >/dev/null 2>&1 && [ -n "$(docker ps -q 2>/dev/null)" ]; then warn "containers already run here; they keep running, but only the panel's edge may publish ports 80/443"; fi
  return 0
}

# ---------- release ----------
resolve_release() {
  if [ -n "$COMMIT" ]; then info "commit $COMMIT (given)"; return 0; fi
  local refs want
  refs="$(git ls-remote --tags "$REPO_URL" 'refs/tags/v*' 2>/dev/null)" || return 1
  if [ -n "$RELEASE" ]; then want="$RELEASE"
  else
    want="$(printf '%s\n' "$refs" | awk '{print $2}' | sed -n 's#^refs/tags/\(v[0-9]*\.[0-9]*\.[0-9]*\)$#\1#p' | sort -V | tail -n1)"
    [ -n "$want" ] || { echo "FAIL no release tag found"; return 1; }
  fi
  ver_ge "${want#v}" "$(min_release)" || { echo "FAIL $want is older than v$(min_release), the first release this installer can install on this server"; return 1; }
  # an annotated tag's commit is on its peeled line (^{}); a plain tag points to the commit itself
  COMMIT="$(printf '%s\n' "$refs" | awk -v t="refs/tags/$want^{}" '$2==t{print $1}')"
  [ -n "$COMMIT" ] || COMMIT="$(printf '%s\n' "$refs" | awk -v t="refs/tags/$want" '$2==t{print $1}')"
  [[ "$COMMIT" =~ ^[0-9a-f]{40}$ ]] || { echo "FAIL release $want not found"; return 1; }
  RELEASE="$want"; pass "release $RELEASE = commit $COMMIT"
}
fetch_release() {
  local dst="$SRC_ROOT/$COMMIT" tmp
  mkdir -p "$SRC_ROOT"
  if ! { [ -d "$dst/.git" ] && [ "$(git -C "$dst" rev-parse HEAD 2>/dev/null)" = "$COMMIT" ] && [ -z "$(git -C "$dst" status --porcelain 2>/dev/null)" ]; }; then
    [ -e "$dst" ] && rm -rf "$dst"
    tmp="$(mktemp -d "$SRC_ROOT/.fetch.XXXX")"
    git -C "$tmp" init -q && git -C "$tmp" fetch -q --depth 1 "$REPO_URL" "$COMMIT" && git -C "$tmp" -c advice.detachedHead=false checkout -q FETCH_HEAD || { rm -rf "$tmp"; echo "FAIL cannot fetch $COMMIT"; return 1; }
    [ "$(git -C "$tmp" rev-parse HEAD)" = "$COMMIT" ] || { rm -rf "$tmp"; echo "FAIL fetched commit does not match"; return 1; }
    mv "$tmp" "$dst"
  fi
  local v; v="$(tr -d '[:space:]' < "$dst/VERSION" 2>/dev/null)"
  [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "FAIL commit has no release VERSION file"; return 1; }
  ver_ge "$v" "$(min_release)" || { echo "FAIL v$v is older than v$(min_release), the first release this installer can install on this server"; return 1; }
  [ -z "$RELEASE" ] && RELEASE="v$v"
  [ "$RELEASE" = "v$v" ] || { echo "FAIL tag $RELEASE holds version v$v"; return 1; }
  if [ -n "$RESTORE" ]; then ver_ge "$v" "$MIN_RESTORE" || { echo "FAIL v$v cannot restore a server backup (needs v$MIN_RESTORE or newer)"; return 1; }; fi
  local pkg
  for pkg in panel-01 site-01; do
    [ -f "$dst/packages/$pkg/$pkg.lock" ] || { echo "FAIL $pkg has no lock file"; return 1; }
    (cd "$dst/packages/$pkg" && sha256sum --quiet -c "$pkg.lock" >/dev/null 2>&1) || { echo "FAIL $pkg script does not match its sha256"; return 1; }
  done
  SRC="$dst"; pass "source $RELEASE fetched (git checked the commit) and the install scripts match their sha256"
}

# ---------- baseline ----------
apt_run() { apt-get "${APT_OPTS[@]}" "$@" < /dev/null >> "$STATE_DIR/apt.log" 2>&1; }
set_timezone() {
  if [ -z "$TZONE" ]; then info "time zone kept: $(timedatectl show -p Timezone --value 2>/dev/null)"; return 0; fi
  timedatectl list-timezones 2>/dev/null | grep -qxF "$TZONE" || { echo "FAIL unknown time zone $TZONE"; return 1; }
  timedatectl set-timezone "$TZONE" && pass "time zone $TZONE"
}
do_updates() {
  apt_run update || return 1
  apt_run upgrade || return 1
  apt_run install unattended-upgrades fail2ban python3-systemd ufw ca-certificates curl gnupg git tmux || return 1
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
  if command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then pass "docker already installed"; else
    install -d -m 0755 "$R/etc/apt/keyrings"
    curl -fsSL "https://download.docker.com/linux/$DISTRO/gpg" -o "$STATE_DIR/docker.asc" || return 1
    local fpr; fpr="$(gpg --show-keys --with-colons "$STATE_DIR/docker.asc" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')"
    [ "$fpr" = "$DOCKER_FPR" ] || { echo "FAIL docker key fingerprint mismatch (${fpr:-none})"; return 1; }
    install -m 0644 "$STATE_DIR/docker.asc" "$R/etc/apt/keyrings/docker.asc"
    local codename arch; codename="$(. "$R/etc/os-release"; echo "${VERSION_CODENAME:-}")"; arch="$(dpkg --print-architecture)"
    echo "deb [arch=$arch signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$DISTRO $codename stable" > "$R/etc/apt/sources.list.d/docker.list"
    apt_run update || return 1
    apt_run install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin || return 1
    pass "docker engine installed (signing key fingerprint verified)"
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
  local ports; ports="$(echo "$SSHP" | xargs | tr ' ' ',')"
  install -d -m 0755 "$R/etc/fail2ban/jail.d"
  printf '[DEFAULT]\nbantime = 1h\nfindtime = 10m\nmaxretry = 5\nbackend = systemd\n\n[sshd]\nenabled = true\nport = %s\n' "$ports" > "$R/etc/fail2ban/jail.d/nostek.local"
  systemctl enable --now fail2ban >/dev/null 2>&1 && systemctl restart fail2ban || return 1
  pass "fail2ban guards SSH on port $ports"
}
# The firewall keeps every SSH port open; a timer turns it off again after 10 minutes unless the install gets past
# the check that the rules are in place, so a broken run cannot lock anyone out.
setup_firewall() {
  local p
  ufw default deny incoming >/dev/null && ufw default allow outgoing >/dev/null || return 1
  for p in $SSHP; do ufw allow "$p/tcp" comment "ssh (nostek)" >/dev/null || return 1; done
  ufw allow 80/tcp comment "http (nostek)" >/dev/null && ufw allow 443/tcp comment "https (nostek)" >/dev/null || return 1
  for p in $SSHP; do ufw show added 2>/dev/null | grep -q -- "allow $p/tcp" || { echo "FAIL ssh rule for $p not staged"; return 1; }; done
  systemd-run --unit="$TIMER_UNIT" --on-active="$REVERT_SECONDS" /usr/sbin/ufw --force disable >/dev/null 2>&1 || { echo "FAIL cannot schedule the firewall safety timer"; return 1; }
  ufw --force enable >/dev/null || return 1
  ufw status 2>/dev/null | grep -q "Status: active" || return 1
  for p in $SSHP; do ufw status 2>/dev/null | grep -qE "^$p/tcp +ALLOW" || { echo "FAIL ssh port $p not open after enable"; return 1; }; done
  systemctl stop "$TIMER_UNIT.timer" >/dev/null 2>&1 || true
  pass "firewall on: SSH ($SSHP) 80, 443 open; everything else closed (port 2222 for SFTP is opened by Docker)"
}
baseline() {
  progress B1/6 "time zone"; set_timezone || die "time zone"
  progress B2/6 "updates (a few minutes)"; do_updates || die "apt failed (see ${STATE_DIR#"$R"}/apt.log)"
  progress B3/6 "swap"; ensure_swap || die "swap"
  progress B4/6 "docker"; install_docker || die "docker"
  progress B5/6 "fail2ban"; setup_fail2ban || die "fail2ban"
  progress B6/6 "firewall"; if ! setup_firewall; then ufw --force disable >/dev/null 2>&1; die "firewall (turned off again)"; fi
  state_set phase BASE_DONE
}

run_pkg() { # $1 package, $2 script, rest: arguments. Prints the package's own lines.
  local pkg="$1" script="$2"; shift 2
  bash "$SRC/packages/$pkg/$script" "$@" --auto < /dev/null 2>&1
}

confirm() {
  [ "$YES" = 1 ] && return 0
  echo "SUMMARY ===== Nostek Panel install ====="
  echo "SUMMARY panel:         https://$DOMAIN"
  echo "SUMMARY preview zone:  *.$ZONE"
  echo "SUMMARY server:        $HOST ($IP)"
  echo "SUMMARY time zone:     ${TZONE:-keep $(timedatectl show -p Timezone --value 2>/dev/null)}"
  echo "SUMMARY release:       ${RELEASE:-newest} ${COMMIT:+($COMMIT)}"
  echo "SUMMARY firewall:      SSH ($SSHP) 80, 443 open; everything else closed"
  [ -n "$RESTORE" ] && echo "SUMMARY restore:       $RESTORE (panel and websites of the old server)"
  echo "SUMMARY ========================================"
  local a=""
  if [ -r /dev/tty ]; then printf 'Type yes to install: ' > /dev/tty; read -r a < /dev/tty || a=""; fi
  [ "$a" = yes ] || refuse "not confirmed (type yes, or add --yes)"
}

# ---------- restore (BAK-02) ----------
# The agent of this release is built (the same image SITE-01 uses later) and checks the whole file in a container
# without network: passphrase, every part, the manifest. Only the base is set at this point; nothing of the panel.
check_backup() {
  local tag="nostek/agent-build:$RELEASE" out rc
  if [ "$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$tag" 2>/dev/null)" != "$COMMIT" ]; then
    info "building the agent $RELEASE to check the server backup (a few minutes)"
    docker build -q --build-arg "VERSION=$RELEASE" --build-arg "REVISION=$COMMIT" -t "$tag" "$SRC/agent" >/dev/null || { echo "FAIL cannot build the agent"; return 1; }
  fi
  out="$(docker run --rm --network none -v "$R$RESTORE:/in/backup.npbk:ro" -v "$PASSF:/in/pass:ro" "$tag" server-restore check --file /in/backup.npbk --passphrase-file /in/pass 2>&1)"; rc=$?
  printf '%s\n' "$out" | sed 's/^/  check | /'
  [ "$rc" = 0 ] && printf '%s\n' "$out" | grep -q '^SERVER_BACKUP ok ' || return 1
  pass "server backup checked: $(printf '%s\n' "$out" | sed -n 's/^SERVER_BACKUP ok //p')"
}
core() { docker compose --project-directory "$R/srv/nostek/core" -f "$R/srv/nostek/core/compose.yml" --env-file "$R/srv/nostek/core/.env" "$@"; }
restore_all() {
  local na="$R/usr/local/sbin/nostek-agent" out rc
  out="$("$na" server-restore sites --file "$R$RESTORE" --passphrase-file "$PASSF" 2>&1)"; rc=$?
  printf '%s\n' "$out" | sed 's/^/  restore | /'
  case "$rc" in 0) ;; 3) warn "some websites did not come back (lines above); their backups are kept" ;; *) die "restoring the websites failed (lines above)" ;; esac
  SITES_LINE="$(printf '%s\n' "$out" | sed -n 's/^SITES //p' | tail -n1)"
  core stop panel >/dev/null 2>&1 || die "cannot stop the panel to put its database back"
  out="$("$na" server-restore panel --file "$R$RESTORE" --passphrase-file "$PASSF" 2>&1)"; rc=$?
  printf '%s\n' "$out" | sed 's/^/  restore | /'
  [ "$rc" = 0 ] || { core start panel >/dev/null 2>&1; die "putting the panel's database back failed (lines above); the panel runs with an empty database"; }
  core start panel >/dev/null 2>&1 || die "the panel does not start on its restored database"
  systemctl restart nostek-agent >/dev/null 2>&1 || warn "restart the agent: systemctl restart nostek-agent"
  pass "panel database, admins and settings restored"
}

install_all() {
  ask_passphrase
  preflight
  [ "$FAILS" = 0 ] || die "checks failed ($FAILS); fix them and run again"
  confirm
  mkdir -p "$STATE_DIR"; state_set phase INSTALLING; state_set started "$(date +%s)"
  progress I1/5 "release"; resolve_release || die "cannot find the release"; fetch_release || die "release check failed"
  state_set release "$RELEASE"; state_set commit "$COMMIT"
  progress I2/5 "server baseline"; baseline
  if [ -n "$RESTORE" ]; then
    progress I2b/5 "check the server backup"
    check_backup || die "the server backup was refused (wrong passphrase, or the file is damaged or incomplete); nothing of the panel is installed yet, so run again with the right file and passphrase"
  fi
  local out
  progress I3/5 "panel (PANEL-01, builds the panel image: 5-10 minutes)"
  out="$(run_pkg panel-01 panel-01-core.sh APPLY --ack PANEL-01 --expect-ip "$IP" --expect-host "$HOST" --domain "$DOMAIN" --src-commit "$COMMIT")"
  printf '%s\n' "$out" | sed 's/^/  panel-01 | /'
  printf '%s\n' "$out" | grep -q '^RESULT PANEL-01 APPLY PASS' || die "panel install failed (lines above)"
  progress I4/5 "agent (SITE-01)"
  out="$(run_pkg site-01 site-01-agent.sh APPLY --ack SITE-01 --expect-ip "$IP" --expect-host "$HOST" --domain "$DOMAIN" --preview-zone "$ZONE" --src-commit "$COMMIT")"
  printf '%s\n' "$out" | sed 's/^/  site-01 | /'
  printf '%s\n' "$out" | grep -q '^RESULT SITE-01 APPLY PASS' || die "agent install failed (lines above)"
  if [ -n "$RESTORE" ]; then
    progress I5/5 "restore websites and panel"
    state_set phase RESTORING
    restore_all
    state_set phase DONE; state_set restored "$(date +%s)"
    echo "SUMMARY ===== Nostek Panel $RELEASE: server restored ====="
    echo "SUMMARY panel: https://$DOMAIN (the admins of the old server sign in as before, two-step login included)"
    echo "SUMMARY websites: ${SITES_LINE:-none}"
    echo "SUMMARY point each website's DNS to $IP; its certificate is issued when it does"
    echo "SUMMARY backups are not set up yet: see docs/guides/install.md"
    echo "SUMMARY ==============================================="
    echo "RESULT $STEP INSTALL PASS version=$RELEASE restore=${SITES_LINE// /,}"
    exit 0
  fi
  progress I5/5 "first admin"
  out="$(run_pkg panel-01 panel-01-core.sh SETUP-LINK --ack PANEL-01 --expect-ip "$IP" --expect-host "$HOST" --domain "$DOMAIN")"
  local link; link="$(printf '%s\n' "$out" | sed -n 's/^SETUP_LINK //p' | head -n1)"
  [ -n "$link" ] || die "no setup link (run: sudo bash $SRC/packages/panel-01/panel-01-core.sh SETUP-LINK --ack PANEL-01 --expect-ip $IP --expect-host $HOST --domain $DOMAIN --auto)"
  state_set phase DONE
  echo "SUMMARY ===== Nostek Panel $RELEASE is installed ====="
  echo "SUMMARY panel: https://$DOMAIN"
  echo "SUMMARY create the first admin within 30 minutes (one use): $link"
  echo "SUMMARY new link later: sudo bash ${SRC#"$R"}/packages/panel-01/panel-01-core.sh SETUP-LINK --ack PANEL-01 --expect-ip $IP --expect-host $HOST --domain $DOMAIN --auto"
  echo "SUMMARY backups are not set up yet: see docs/guides/install.md"
  echo "SUMMARY ==============================================="
  echo "RESULT $STEP INSTALL PASS version=$RELEASE"
  exit 0
}

check_invocation
mkdir -p "$(dirname "$LOG")"; touch "$LOG"; chmod 600 "$LOG"
exec > >(tee -a "$LOG") 2>&1
echo "INFO $STEP installer $INSTALLER_VERSION, $MODE, $(date '+%Y-%m-%d %H:%M:%S %Z')"
case "$MODE" in
  PREFLIGHT) preflight; if [ "$FAILS" = 0 ]; then echo "RESULT $STEP PREFLIGHT PASS"; exit 0; fi
    echo "RESULT $STEP PREFLIGHT FAIL failed=$FAILS"; exit 1 ;;
  INSTALL) install_all ;;
esac
