#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# INST-01 harness: runs the installer against a fake root with stubbed system commands and a local test
# repository whose PANEL-01 and SITE-01 scripts only record how they were called.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../nostek-install.sh}")"
[ "$(id -u)" = 0 ] || { echo "run with sudo"; exit 2; }
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"; trap 'rm -rf "$TMP_ALL"' EXIT

# ---------- test repository: v1.31.0, v1.32.0 (annotated), v1.32.1 (plain tag), one tampered commit ----------
REPO="$TMP_ALL/repo"; mkdir -p "$REPO/packages/panel-01" "$REPO/packages/site-01"
G() { git -C "$REPO" -c user.name=test -c user.email=test@example.test "$@"; }
fake_pkg() { # $1 package, $2 script
  cat > "$REPO/packages/$1/$2" <<'PKG'
#!/usr/bin/env bash
# fake package: records its arguments; answers like the real one
name="$(basename "$0")"; echo "$name $*" >> "$FAKE/pkg.log"
mode="$1"; step="$(echo "$name" | cut -d- -f1-2 | tr a-z A-Z)"
[ -f "$FAKE/break_$step" ] && { echo "RESULT $step $mode FAIL fake"; exit 1; }
[ "$mode" = SETUP-LINK ] && { echo "SETUP_LINK https://panel.example.test/setup/one-time-test"; echo "RESULT $step SETUP-LINK PASS"; exit 0; }
echo "RESULT $step $mode PASS version=v$(cat "$(dirname "$0")/../../VERSION")"
PKG
  (cd "$REPO/packages/$1" && sha256sum "$2" > "$1.lock")
}
G init -q; G config uploadpack.allowAnySHA1InWant true
fake_pkg panel-01 panel-01-core.sh; fake_pkg site-01 site-01-agent.sh
echo 1.31.0 > "$REPO/VERSION"; G add -A; G commit -qm v1.31.0; G tag v1.31.0
echo 1.32.0 > "$REPO/VERSION"; G commit -qam v1.32.0; G tag -a -m "v1.32.0" v1.32.0; C132="$(G rev-parse HEAD)"
echo 1.32.1 > "$REPO/VERSION"; G commit -qam v1.32.1; G tag v1.32.1; C1321="$(G rev-parse HEAD)"
echo "# changed" >> "$REPO/packages/panel-01/panel-01-core.sh"; G commit -qam tampered; CBAD="$(G rev-parse HEAD)"
G checkout -q v1.32.1

new_env() {
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export INST01_TEST_ROOT="$T/root" INST01_TEST_REPO="file://$REPO" FAKE="$T/fake"
  local r="$INST01_TEST_ROOT"
  mkdir -p "$FAKE" "$r"/etc/apt/apt.conf.d "$r"/etc/apt/sources.list.d "$r"/etc/systemd/system "$r"/proc
  printf 'ID=ubuntu\nVERSION_ID="24.04"\nVERSION_CODENAME=noble\n' > "$r/etc/os-release"
  printf 'MemTotal:        4000000 kB\n' > "$r/proc/meminfo"
  touch "$r/etc/fstab"; : > "$FAKE/ufw_rules"
  export PATH="$HERE/stubs:/usr/sbin:/usr/bin:/sbin:/bin"
  export FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=10.0.0.5 FAKE_PUBLIC_IP=198.51.100.7 FAKE_DNS_IP=198.51.100.7 FAKE_WILD_IP=198.51.100.7
  export FAKE_SSH_PORTS="22 20022" FAKE_DISK=40000
  unset FAKE_FPR || true
}
A=(--domain panel.example.test)
run() { OUT="$(bash "$SCRIPT" "$@" 2>&1 </dev/null)"; RC=$?; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else echo "FAIL t$TESTS $1"; echo "$OUT" | tail -n 25 | sed 's/^/    | /'; BAD=$((BAD+1)); fi; }
has() { grep -q -- "$1" <<<"$OUT"; }
phase() { grep '^phase=' "$INST01_TEST_ROOT/var/lib/nostek/inst-01/state" 2>/dev/null | tail -n1 | cut -d= -f2; }
apt_ran() { grep -q '^apt-get' "$FAKE/argv.log" 2>/dev/null; }

# ---------- PREFLIGHT ----------
new_env; run PREFLIGHT "${A[@]}"; ok "preflight passes" '[ $RC = 0 ] && has "RESULT INST-01 PREFLIGHT PASS"'
ok "preview zone derived from the panel name" 'has "\*.preview.example.test points to 198.51.100.7"'
ok "preflight changes nothing" '! apt_ran && [ ! -f "$FAKE/ufw_on" ] && [ -z "$(phase)" ]'
new_env; printf 'ID=debian\nVERSION_ID="12"\n' > "$INST01_TEST_ROOT/etc/os-release"; run PREFLIGHT "${A[@]}"; ok "other system refused" '[ $RC = 1 ] && has "needs Ubuntu 22.04 or 24.04"'
new_env; FAKE_DISK=8000; run PREFLIGHT "${A[@]}"; ok "small disk fails" '[ $RC = 1 ] && has "needs at least 15 GB"'
new_env; printf 'MemTotal: 500000 kB\n' > "$INST01_TEST_ROOT/proc/meminfo"; run PREFLIGHT "${A[@]}"; ok "too little memory fails" '[ $RC = 1 ] && has "needs at least 1 GB memory"'
new_env; FAKE_DNS_IP=203.0.113.9; run PREFLIGHT "${A[@]}"; ok "panel name elsewhere fails" '[ $RC = 1 ] && has "must have an A record to 198.51.100.7 (found: 203.0.113.9"'
new_env; FAKE_WILD_IP=""; run PREFLIGHT "${A[@]}"; ok "missing preview wildcard is a warning" '[ $RC = 0 ] && has "WARN \*.preview.example.test does not point here"'
new_env; touch "$FAKE/busy80"; run PREFLIGHT "${A[@]}"; ok "port 80 in use fails" '[ $RC = 1 ] && has "ports 80/443/2222 are in use"'
new_env; FAKE_PUBLIC_IP=""; run PREFLIGHT "${A[@]}"; ok "no public address: asks for --ip" '[ $RC = 1 ] && has "give it with --ip"'
new_env; FAKE_PUBLIC_IP=""; run PREFLIGHT "${A[@]}" --ip 198.51.100.7; ok "--ip is used" '[ $RC = 0 ]'
new_env; touch "$FAKE/no_github"; run PREFLIGHT "${A[@]}"; ok "unreachable download source fails" '[ $RC = 1 ] && has "cannot reach github.com"'
new_env; run PREFLIGHT --domain "panel_bad"; ok "bad panel name refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT --domain example.test; ok "no parent zone: asks for --preview-zone" '[ $RC = 3 ] && has "preview-zone is needed"'
new_env; run PREFLIGHT "${A[@]}" --timezone "Asia/Jakarta; rm"; ok "odd time zone refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT "${A[@]}" --release v1.32.0 --src-commit "$C132"; ok "release and commit together refused" '[ $RC = 3 ]'

# ---------- INSTALL ----------
new_env; run INSTALL "${A[@]}"; ok "install without --yes and no terminal stops before changing anything" '[ $RC = 3 ] && has "not confirmed" && ! apt_ran && [ ! -f "$FAKE/ufw_on" ]'
new_env; run INSTALL "${A[@]}" --yes --timezone Asia/Makassar
ok "install passes with the newest release" '[ $RC = 0 ] && has "RESULT INST-01 INSTALL PASS version=v1.32.1" && [ "$(phase)" = DONE ] && has "commit $C1321"'
ok "panel got this server, the name and the commit" 'grep -q "^panel-01-core.sh APPLY --ack PANEL-01 --expect-ip 198.51.100.7 --expect-host fake-vps --domain panel.example.test --src-commit $C1321 --auto$" "$FAKE/pkg.log"'
ok "agent got the preview zone, after the panel" 'grep -q "^site-01-agent.sh APPLY .*--preview-zone preview.example.test --src-commit $C1321 --auto$" "$FAKE/pkg.log" && [ "$(grep -n "^panel-01-core.sh APPLY" "$FAKE/pkg.log" | cut -d: -f1)" -lt "$(grep -n "^site-01-agent.sh APPLY" "$FAKE/pkg.log" | cut -d: -f1)" ]'
ok "setup link shown" 'has "create the first admin within 30 minutes (one use): https://panel.example.test/setup/one-time-test"'
ok "time zone set" '[ "$(cat "$FAKE/tz")" = Asia/Makassar ]'
ok "firewall: every SSH port, 80 and 443; safety timer stopped" 'grep -qx 22/tcp "$FAKE/ufw_rules" && grep -qx 20022/tcp "$FAKE/ufw_rules" && grep -qx 80/tcp "$FAKE/ufw_rules" && grep -qx 443/tcp "$FAKE/ufw_rules" && [ -f "$FAKE/ufw_on" ] && [ ! -f "$FAKE/timer" ] && [ "$(grep -n "^systemd-run" "$FAKE/argv.log" | head -1 | cut -d: -f1)" -lt "$(grep -n "^ufw --force enable" "$FAKE/argv.log" | head -1 | cut -d: -f1)" ]'
ok "fail2ban guards both SSH ports" 'grep -qx "port = 22,20022" "$INST01_TEST_ROOT/etc/fail2ban/jail.d/nostek.local"'
ok "docker repository signed-by, log limits" 'grep -q "signed-by=/etc/apt/keyrings/docker.asc" "$INST01_TEST_ROOT/etc/apt/sources.list.d/docker.list" && grep -q "\"max-size\": \"20m\"" "$INST01_TEST_ROOT/etc/docker/daemon.json"'
ok "apt is non-interactive; swap added" '[ -z "$(grep "^apt-get" "$FAKE/argv.log" | grep -v -- "Dpkg::Use-Pty=0")" ] && [ -f "$FAKE/swap" ]'
ok "baseline recorded for PANEL-01, log is 600" 'grep -qx "release=v1.32.1" "$INST01_TEST_ROOT/var/lib/nostek/inst-01/state" && [ "$(stat -c %a "$INST01_TEST_ROOT/var/log/nostek-install.log")" = 600 ] && grep -q "RESULT INST-01 INSTALL PASS" "$INST01_TEST_ROOT/var/log/nostek-install.log"'
run INSTALL "${A[@]}" --yes; ok "second install refused" '[ $RC = 1 ] && has "already installed (version v1.32.1)"'

new_env; run INSTALL "${A[@]}" --yes --release v1.32.0; ok "annotated release tag gives its commit" '[ $RC = 0 ] && has "release v1.32.0 = commit $C132" && grep -q -- "--src-commit $C132 " "$FAKE/pkg.log"'
new_env; run INSTALL "${A[@]}" --yes --release v1.31.0; ok "release older than the installer refused before any change" '[ $RC = 1 ] && has "older than v1.32.0" && ! apt_ran'
new_env; run INSTALL "${A[@]}" --yes --release v9.9.9; ok "unknown release fails" '[ $RC = 1 ] && has "release v9.9.9 not found" && ! apt_ran'
new_env; run INSTALL "${A[@]}" --yes --src-commit "$CBAD"; ok "script not matching its sha256 stops before any change" '[ $RC = 1 ] && has "panel-01 script does not match its sha256" && ! apt_ran && [ ! -f "$FAKE/pkg.log" ]'
new_env; run INSTALL "${A[@]}" --yes --src-commit "$C132"; ok "--src-commit installs that commit" '[ $RC = 0 ] && has "version=v1.32.0"'
new_env; export FAKE_FPR=0000000000000000000000000000000000000000; run INSTALL "${A[@]}" --yes; ok "wrong docker key stops" '[ $RC = 1 ] && has "fingerprint mismatch" && [ ! -f "$INST01_TEST_ROOT/etc/apt/sources.list.d/docker.list" ] && [ ! -f "$FAKE/pkg.log" ]'
new_env; run INSTALL "${A[@]}" --yes --timezone Mars/Olympus; ok "unknown time zone stops" '[ $RC = 1 ] && has "unknown time zone"'
new_env; touch "$FAKE/break_timer"; run INSTALL "${A[@]}" --yes; ok "no safety timer: firewall stays off" '[ $RC = 1 ] && [ ! -f "$FAKE/ufw_on" ] && [ ! -f "$FAKE/pkg.log" ]'
new_env; touch "$FAKE/break_apt"; run INSTALL "${A[@]}" --yes; ok "apt failure stops before the firewall" '[ $RC = 1 ] && [ ! -f "$FAKE/ufw_on" ]'
new_env; touch "$FAKE/break_PANEL-01"; run INSTALL "${A[@]}" --yes; ok "panel failure is FAIL, agent not installed" '[ $RC = 1 ] && has "panel install failed" && [ "$(phase)" = BASE_DONE ] && ! grep -q "^site-01" "$FAKE/pkg.log"'
run INSTALL "${A[@]}" --yes; ok "a failed install can run again" '[ $RC = 1 ] || true; rm -f "$FAKE/break_PANEL-01"; run INSTALL "${A[@]}" --yes; [ $RC = 0 ] && [ "$(phase)" = DONE ]'
new_env; mkdir -p "$INST01_TEST_ROOT/var/lib/nostek/panel-01"; echo phase=APPLIED > "$INST01_TEST_ROOT/var/lib/nostek/panel-01/state"; run INSTALL "${A[@]}" --yes; ok "a server with the panel already is refused" '[ $RC = 1 ] && has "already installed here" && ! apt_ran'

# ---------- RESTORE (BAK-02): v1.34.0 can restore a server backup ----------
G checkout -q v1.32.1; echo 1.34.0 > "$REPO/VERSION"; G commit -qam v1.34.0; G tag v1.34.0; C134="$(G rev-parse HEAD)"
PASSPHRASE="a long test passphrase"
restore_env() { # a server backup, its passphrase file and a fake installed agent that records its calls
  new_env
  local r="$INST01_TEST_ROOT"
  mkdir -p "$r/root" "$r/usr/local/sbin"
  printf 'NPBK1\nsealed' > "$r/root/old.npbk"; printf '%s\n' "$PASSPHRASE" > "$r/root/pass"; chmod 600 "$r/root/pass"
  printf 'SITE voltix-id voltix.id docker\nSERVER_BACKUP ok sites=1 panel=v1.34.0 host=old-vps made=2026-11-26_17:00_WIB\n' > "$FAKE/check_out"
  cat > "$r/usr/local/sbin/nostek-agent" <<'AG'
#!/usr/bin/env bash
echo "nostek-agent $*" >> "$FAKE/argv.log"
pf=""; while [ $# -gt 0 ]; do [ "$1" = --passphrase-file ] && pf="$2"; shift; done
cat "$pf" > "$FAKE/agent_pass"
case "$(grep -c . "$FAKE/argv.log" >/dev/null; tail -n1 "$FAKE/argv.log" | awk '{print $3}')" in
  sites) [ -f "$FAKE/break_sites" ] && { echo "SITES refused: boom"; exit 1; }
    echo "STEP website voltix-id: settings"; echo "SITE voltix-id restored"; echo "SITES restored=1 skipped=0 failed=0" ;;
  panel) echo "PANEL restored" ;;
esac
AG
  chmod 755 "$r/usr/local/sbin/nostek-agent"
}
RA=(--domain panel.example.test --yes --release v1.34.0 --restore /root/old.npbk --passphrase-file /root/pass)
restore_env; run PREFLIGHT --domain panel.example.test --restore /root/old.npbk --passphrase-file /root/pass
ok "restore preflight checks the file and the passphrase" '[ $RC = 0 ] && has "PASS server backup /root/old.npbk" && has "PASS passphrase file (22 characters)"'
restore_env; printf 'PK\003\004zip' > "$INST01_TEST_ROOT/root/old.npbk"; run PREFLIGHT --domain panel.example.test --restore /root/old.npbk
ok "a file that is not a server backup fails" '[ $RC = 1 ] && has "is not a Nostek server backup"'
restore_env; echo short > "$INST01_TEST_ROOT/root/pass"; run PREFLIGHT --domain panel.example.test --restore /root/old.npbk --passphrase-file /root/pass
ok "a short passphrase fails" '[ $RC = 1 ] && has "fewer than 12 characters"'
restore_env; mkdir -p "$INST01_TEST_ROOT/srv/nostek/sites/old"; run PREFLIGHT --domain panel.example.test --restore /root/old.npbk
ok "a server with websites is refused for a restore" '[ $RC = 1 ] && has "has websites already"'
restore_env; run INSTALL --domain panel.example.test --yes --restore /root/none.npbk; ok "missing server backup refused" '[ $RC = 3 ] && has "no readable file /root/none.npbk"'
restore_env; run INSTALL "${A[@]}" --yes --passphrase-file /root/pass; ok "--passphrase-file alone refused" '[ $RC = 3 ]'
restore_env; run INSTALL --domain panel.example.test --yes --release v1.34.0 --restore /root/old.npbk; ok "no terminal and no passphrase file: refused before any change" '[ $RC = 3 ] && has "passphrase-file" && ! apt_ran'
restore_env; run INSTALL --domain panel.example.test --yes --release v1.32.1 --restore /root/old.npbk --passphrase-file /root/pass
ok "a release before v1.34.0 cannot restore, nothing changed" '[ $RC = 1 ] && has "cannot restore a server backup (needs v1.34.0" && ! apt_ran'
restore_env; echo 1 > "$FAKE/check_rc"; echo "SERVER_BACKUP refused: wrong passphrase, or the file was changed" > "$FAKE/check_out"; run INSTALL "${RA[@]}"
ok "wrong passphrase stops before the panel is installed" '[ $RC = 1 ] && has "server backup was refused" && has "check | SERVER_BACKUP refused: wrong passphrase" && [ "$(phase)" = BASE_DONE ] && [ ! -f "$FAKE/pkg.log" ]'
rm -f "$FAKE/check_rc"; printf 'SITE voltix-id voltix.id docker\nSERVER_BACKUP ok sites=1 panel=v1.34.0 host=old-vps made=2026-11-26_17:00_WIB\n' > "$FAKE/check_out"; run INSTALL "${RA[@]}"
ok "restore runs again after a refused check and passes" '[ $RC = 0 ] && has "RESULT INST-01 INSTALL PASS version=v1.34.0 restore=restored=1,skipped=0,failed=0" && [ "$(phase)" = DONE ]'
ok "check ran in a container without network, passphrase mounted from a file" 'grep -q "^docker run --rm --network none .*/root/pass:/in/pass:ro nostek/agent-build:v1.34.0 server-restore check --file /in/backup.npbk --passphrase-file /in/pass$" "$FAKE/argv.log" && [ "$(cat "$FAKE/check_pass")" = "$PASSPHRASE" ]'
ok "websites first, then the panel stopped, its database put back, started" 'L() { grep -n "$1" "$FAKE/argv.log" | head -1 | cut -d: -f1; }; [ "$(L "nostek-agent server-restore sites")" -lt "$(L "stop panel")" ] && [ "$(L "stop panel")" -lt "$(L "nostek-agent server-restore panel")" ] && [ "$(L "nostek-agent server-restore panel")" -lt "$(L "start panel")" ] && [ "$(cat "$FAKE/agent_pass")" = "$PASSPHRASE" ]'
ok "panel and agent installed from v1.34.0, no admin link" 'grep -q "^panel-01-core.sh APPLY .*--src-commit $C134 --auto$" "$FAKE/pkg.log" && grep -q "^site-01-agent.sh APPLY" "$FAKE/pkg.log" && ! grep -q SETUP-LINK "$FAKE/pkg.log" && has "sign in as before" && ! has "create the first admin"'
ok "the passphrase is in no command line and no log" '! grep -qF "$PASSPHRASE" "$FAKE/argv.log" "$INST01_TEST_ROOT/var/log/nostek-install.log" "$FAKE/pkg.log"'
restore_env; touch "$FAKE/break_sites"; run INSTALL "${RA[@]}"
ok "a failed website restore is FAIL, the panel database is left alone" '[ $RC = 1 ] && has "restoring the websites failed" && ! grep -q "server-restore panel" "$FAKE/argv.log" && [ "$(phase)" = RESTORING ]'

rm -rf "$TMP_ALL"/t.*; LEFT="$(find "$TMP_ALL" -mindepth 1 -maxdepth 1 ! -name repo | wc -l)"
if [ "$BAD" -eq 0 ]; then echo "RESULT INST-01 HARNESS PASS tests=$TESTS sisa_uji=$LEFT"; exit 0; fi
echo "RESULT INST-01 HARNESS FAIL failed=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
