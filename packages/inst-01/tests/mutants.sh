#!/usr/bin/env bash
# Mutation check: each mutant breaks one safety property; the harness must FAIL for every mutant.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; SRC="$HERE/../nostek-install.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
MUTANTS=(
  's/sha256sum --quiet -c "$pkg.lock" >\/dev\/null 2>&1)/true)/'                       # scripts not checked
  's/\[ "$fpr" = "$DOCKER_FPR" \] ||/true ||/'                                          # docker key not verified
  's/  for p in $SSHP; do ufw allow "$p\/tcp"/  for p in 22; do ufw allow "$p\/tcp"/'   # only port 22 kept open
  's/  systemd-run --unit="$TIMER_UNIT"/  true --unit="$TIMER_UNIT"/'                  # no firewall safety timer
  's/  \[ "$a" = yes \] || refuse/  true || refuse/'                                     # installs without consent
  's/ver_ge "[^"]*" "$(min_release)" ||/true ||/g'                                # too old a release
  's/printf .%s\\n. $a | grep -qxF "$IP"; then pass "$DOMAIN points/true; then pass "$DOMAIN points/'  # DNS not checked
  's/if \[ "$(state_get phase)" = DONE \]; then fail "already installed/if false; then fail "already installed/'  # installs twice
  's/chmod 600 "$LOG"/chmod 644 "$LOG"/'                                                # log readable by all
  's/    check_backup || die/    true || die/'                                           # server backup not checked first
  's/ver_ge "$v" "$MIN_RESTORE" ||/true ||/'                                               # old release restores
  's/  core stop panel >\/dev\/null 2>\&1 || die/  true || die/'                             # panel database replaced while it runs
  's/\[ "$n" -ge 12 \]; then pass "passphrase file/true; then pass "passphrase file/'       # short passphrase accepted
  's/"debian 12") DISTRO=debian;/"debian 1"*) DISTRO=debian;/'                            # any Debian accepted
  's/"debian 12") DISTRO=debian;/"debian 12") DISTRO=ubuntu;/'                              # Debian gets the Ubuntu Docker repository
  's/then echo "$MIN_INST02"; else/then echo "$MIN_RELEASE"; else/'                       # Debian or arm64 gets a release not built for it
)
killed=0; n=0
for m in "${MUTANTS[@]}"; do
  n=$((n+1)); f="$TMP/m$n.sh"; sed "$m" "$SRC" > "$f"
  if cmp -s "$f" "$SRC"; then echo "FAIL mutant $n did not apply: $m"; continue; fi
  if bash "$HERE/harness.sh" "$f" >/dev/null 2>&1; then echo "FAIL mutant $n survived: $m"; else echo "PASS mutant $n killed"; killed=$((killed+1)); fi
done
rm -f "$TMP"/m*.sh
if [ "$killed" -eq "$n" ]; then echo "RESULT INST-01 MUTANTS PASS killed=$killed/$n sisa_uji=0"; exit 0; fi
echo "RESULT INST-01 MUTANTS FAIL killed=$killed/$n"; exit 1
