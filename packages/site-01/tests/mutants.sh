#!/usr/bin/env bash
# Mutation check: each mutant breaks one safety property; the tests must FAIL for every mutant.
# Part 1 mutates the agent's Go code (input checks, peer check, secret handling) and runs `go test`.
# Part 2 mutates the package script and runs the harness (stops at the first failing test).
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"; REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
SRC="$HERE/../site-01-agent.sh"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
killed=0; n=0

# file|old|new (old must occur in the file)
GO_MUTANTS=(
  'internal/spec/spec.go|labelRe = regexp.MustCompile(`^[a-z0-9]|labelRe = regexp.MustCompile(`^[a-zA-Z0-9_]'          # upper case / underscore domains
  'internal/spec/spec.go|if reserved[h] {|if false && reserved[h] {'                                                  # a used domain can be taken again
  'internal/spec/spec.go|titleRe = regexp.MustCompile(`^[\p{L}\p{N} .,:;!?&()/+_@#%-]+$`)|titleRe = regexp.MustCompile(`^.+$`)'  # title may carry shell/yaml characters
  'internal/spec/spec.go|	case "apache", "nginx":|	case "apache", "nginx", "caddy":'                                     # web server outside the list
  'internal/api/api.go|	dec.DisallowUnknownFields()|	// unknown fields accepted'                                         # extra fields (image, volumes) accepted
  'internal/api/api.go|if s.Allowed != nil && !s.Allowed[uid] {|if false {'                                             # any local user may call the agent
  'internal/sites/jobs.go|	j.secret, j.v.HasSecret = "", false|	j.v.HasSecret = false'                                  # password can be fetched twice
  'internal/sites/manager.go|		if a != m.cfg.PublicIPv4 {|		if false {'                                                     # a split A record counts as pointing here
  'internal/sites/manager.go|"--admin_email="+s.AdminEmail, "--skip-email", "--prompt=admin_password")|"--admin_email="+s.AdminEmail, "--skip-email", "--admin_password="+pw)'  # password in process arguments
  'internal/catalog/catalog.go|MariaDB = "mariadb:11.8@sha256:|MariaDB = "mariadb:11.8" + "@x'                            # image not pinned by digest
)
for m in "${GO_MUTANTS[@]}"; do
  n=$((n+1)); file="${m%%|*}"; rest="${m#*|}"; old="${rest%%|*}"; new="${rest#*|}"
  d="$TMP/go$n"; cp -r "$REPO/agent" "$d"
  if ! OLD="$old" NEW="$new" python3 - "$d/$file" <<'PY'
import os, sys
p = sys.argv[1]; s = open(p).read(); o = os.environ["OLD"]
if s.count(o) != 1: sys.exit(1)
open(p, "w").write(s.replace(o, os.environ["NEW"]))
PY
  then echo "FAIL mutant $n did not apply: $file $old"; continue; fi
  if (cd "$d" && go test ./... >/dev/null 2>&1); then echo "FAIL mutant $n survived: $file $old"; else echo "PASS mutant $n killed ($file)"; killed=$((killed+1)); fi
done

SH_MUTANTS=(
  's/\[ "$h" = "$EXPECT_HOST" \] || wrong_host/true/'                                    # host name not checked
  's/if \[ "$(panel_phase)" = APPLIED \]; then/if true; then/'                            # runs without the panel edge
  's/  "socket_gid": $PANEL_UID,/  "socket_gid": 0,/'                                     # panel user cannot open the socket
  's/  mkdir -p "$NOSTEK\/trash"; chmod 0700/  mkdir -p "$NOSTEK\/trash"; chmod 0755/'    # trash (old databases) readable by all
)
[ "${MUTANTS_GO_ONLY:-0}" = 1 ] && SH_MUTANTS=()   # local quick run
for m in "${SH_MUTANTS[@]}"; do
  n=$((n+1)); f="$TMP/m$n.sh"; sed "$m" "$SRC" > "$f"
  if cmp -s "$f" "$SRC"; then echo "FAIL mutant $n did not apply: $m"; continue; fi
  if HARNESS_FAST_FAIL=1 bash "$HERE/harness.sh" "$f" >/dev/null 2>&1; then echo "FAIL mutant $n survived: $m"; else echo "PASS mutant $n killed"; killed=$((killed+1)); fi
done
if [ "$killed" -eq "$n" ]; then echo "RESULT SITE-01 MUTANTS PASS killed=$killed/$n sisa_uji=0"; exit 0; fi
echo "RESULT SITE-01 MUTANTS FAIL killed=$killed/$n"; exit 1
