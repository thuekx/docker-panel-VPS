#!/usr/bin/env bash
# Mutation check: each mutant breaks one safety property; the harness must FAIL for every mutant.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; SRC="$HERE/../panel-01-core.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
MUTANTS=(
  's/\[ "$h" = "$EXPECT_HOST" \] || wrong_host/true/'                                 # host name not checked
  's/if \[ "$(host02_phase)" = DONE \]; then/if true; then/'                           # runs before backups are proven
  's/\[ -z "$f" \] \&\& pass "only edge publishes ports" ||/true ||/'                  # port isolation not verified
  's/chmod 600 "$CORE\/.env"/chmod 644 "$CORE\/.env"/'                                 # settings readable by all
  's/if \[ -d "$CORE" \]; then mv "$CORE" "$BK\/core" \&\& pass/if [ -d "$CORE" ]; then rm -rf "$CORE" \&\& pass/'  # revert deletes instead of moving aside
  's/printf .%s\\n. $a | grep -qxF "$EXPECT_IP"/true/'                                 # DNS not checked
)
killed=0; n=0
for m in "${MUTANTS[@]}"; do
  n=$((n+1)); f="$TMP/m$n.sh"; sed "$m" "$SRC" > "$f"
  if cmp -s "$f" "$SRC"; then echo "FAIL mutant $n did not apply: $m"; continue; fi
  if bash "$HERE/harness.sh" "$f" >/dev/null 2>&1; then echo "FAIL mutant $n survived: $m"; else echo "PASS mutant $n killed"; killed=$((killed+1)); fi
done
rm -f "$TMP"/m*.sh
if [ "$killed" -eq "$n" ]; then echo "RESULT PANEL-01 MUTANTS PASS killed=$killed/$n sisa_uji=0"; exit 0; fi
echo "RESULT PANEL-01 MUTANTS FAIL killed=$killed/$n"; exit 1
