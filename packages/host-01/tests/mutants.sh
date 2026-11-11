#!/usr/bin/env bash
# Mutation check: each mutant breaks one safety property; the harness must FAIL for every mutant.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; SRC="$HERE/../host-01-user-ssh.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
MUTANTS=(
  's/      wrong_host$/      true/'                                   # wrong-host stop removed
  's/\\nPort 22\\nPort %s/\\nPort %s/'                                     # APPLY closes 22 too early
  's/  schedule_revert || {/  true || {/'                            # no safety timer
  's/\[ "${SUDO_USER:-}" = "$USERNAME" \] || refuse "CONFIRM/true || refuse "CONFIRM/'  # confirm from any session
  's/^set -u   # no pipefail.*/set -uo pipefail/'                     # SIGPIPE breaks ufw detection
  's/passwd -l "$OLD_USER"/true/'                                     # old user not locked
  's/if \[ "$AUTO" = "0" \] \&\& \[ -z "${SUDO_USER:-}" \]/if false/'      # root shell allowed
  's/if \[ -n "$PW" \] \&\& \[ "${#PW}" -lt 12 \]/if false/'              # short passwords allowed
)
killed=0; n=0
for m in "${MUTANTS[@]}"; do
  n=$((n+1)); f="$TMP/m$n.sh"; sed "$m" "$SRC" > "$f"
  if cmp -s "$f" "$SRC"; then echo "FAIL mutant $n did not apply: $m"; continue; fi
  if bash "$HERE/harness.sh" "$f" >/dev/null 2>&1; then echo "FAIL mutant $n survived: $m"; else echo "PASS mutant $n killed"; killed=$((killed+1)); fi
done
rm -f "$TMP"/m*.sh
if [ "$killed" -eq "$n" ]; then echo "RESULT HOST-01 MUTANTS PASS killed=$killed/$n sisa_uji=0"; exit 0; fi
echo "RESULT HOST-01 MUTANTS FAIL killed=$killed/$n"; exit 1
