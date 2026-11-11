#!/usr/bin/env bash
# Mutation check: each mutant breaks one safety property; the harness must FAIL for every mutant.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; SRC="$HERE/../host-02-baseline.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
MUTANTS=(
  's/\[ "$h" = "$EXPECT_HOST" \] || wrong_host/true/'                       # host name not checked
  's/  ufw allow "$SSH_PORT\/tcp" comment/  true "$SSH_PORT\/tcp" comment/'  # ssh rule missing
  's/  systemd-run --unit="$TIMER_UNIT"/  true --unit="$TIMER_UNIT"/'       # no firewall auto-revert
  's/\[ "$fpr" = "$DOCKER_FPR" \] ||/true ||/'                              # docker key not verified
  's/^set -u   # no pipefail.*/set -uo pipefail/'                           # SIGPIPE breaks ufw detection
  's/\[ "$GOT" = "$SUM" \] || die/true || die/'                             # restore not compared
  's/restic forget --prune "${TIDS\[@\]}"/true/'                            # test snapshot left behind
  's/chmod 600 "$BK_ETC\/restic.env"/chmod 644 "$BK_ETC\/restic.env"/'      # secrets readable
  's/ss -tnH state established "( sport = :$SSH_PORT )" 2>\/dev\/null | grep -q . ||/true ||/'  # confirm without new session
)
killed=0; n=0
for m in "${MUTANTS[@]}"; do
  n=$((n+1)); f="$TMP/m$n.sh"; sed "$m" "$SRC" > "$f"
  if cmp -s "$f" "$SRC"; then echo "FAIL mutant $n did not apply: $m"; continue; fi
  if bash "$HERE/harness.sh" "$f" >/dev/null 2>&1; then echo "FAIL mutant $n survived: $m"; else echo "PASS mutant $n killed"; killed=$((killed+1)); fi
done
rm -f "$TMP"/m*.sh
if [ "$killed" -eq "$n" ]; then echo "RESULT HOST-02 MUTANTS PASS killed=$killed/$n sisa_uji=0"; exit 0; fi
echo "RESULT HOST-02 MUTANTS FAIL killed=$killed/$n"; exit 1
