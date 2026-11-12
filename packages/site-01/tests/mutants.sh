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
  'internal/sites/files.go|r, err := os.OpenRoot(m.wpDir(id))|r, err := os.OpenRoot(m.dir(id))'                          # file manager reaches secrets and database files
  'internal/sites/files.go|v > 0o777 {|v > 0o7777 {'                                                                     # setuid/setgid permissions allowed
  'internal/sites/files.go|	if rel == "." {
		return FileTrash{}|	if false {
		return FileTrash{}'                                                                                                   # the website folder itself can be deleted
  'internal/sites/db.go|const client = `MYSQL_PWD="$(cat /run/secrets/db_password)" exec "$0" -u"$MARIADB_USER" "$@"`|const client = `exec "$0" -u"$MARIADB_USER" -p"$(cat /run/secrets/db_password)" "$@"`'  # database password in process arguments
  'internal/sites/db.go|	backup, err := m.dump(ctx, id, "before-import")
	if err != nil {|	backup, err := m.dump(ctx, id, "before-import")
	if false {'                                                                                                               # import runs without a safety copy
  'internal/api/files.go|detail := fmt.Sprintf("%s (%d chars)", firstWord(q.SQL), len(q.SQL))|detail := q.SQL'           # SQL text (may hold data) in the audit log
  'internal/sites/wp.go|itemRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)|itemRe = regexp.MustCompile(`^.{1,200}$`)'  # a flag (--require=…) passes as a plugin name
  'internal/sites/wp.go|strings.ReplaceAll(s, "'"'"'", |strings.ReplaceAll(s, "@@@", '                              # quotes break out of the WP-CLI argument list
  'internal/sites/wp.go|"NP_HASH": hex.EncodeToString(sum[:])|"NP_HASH": tok'                                          # login token itself stored and passed to docker
  'internal/sites/wp.go|		for _, f := range []string{"--ssh", "--http", "--path"} {|		for _, f := range []string{} {'                  # console reaches other machines or paths
  'internal/api/wp.go|	if args, err := sites.SplitArgs(a.Line); err == nil && cmdWord.MatchString(args[0]) {|	if args, err := sites.SplitArgs(a.Line); err == nil {
		first = a.Line
	} else if err == nil {'                                                                                                   # console arguments (passwords) in the audit log
  'internal/sites/wp.go|	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, d)|	ctx, cancel := context.WithTimeout(ctx, d)'                                                                           # two changes run at once
  'internal/sites/backup.go|		if part == ".." {|		if false && part == ".." {'                                                         # BAK-01: archive path leaves the website
  'internal/sites/backup.go|		safe, err := m.backup(ctx, j, s, "before-restore")|		safe, err := "", error(nil)'                       # BAK-01: restore without a safety copy
  'internal/sites/backup.go|strings.NewReader(resetSQL(s.DBName()))|strings.NewReader("")'                                                       # BAK-01: tables made after the backup stay behind
  'internal/sites/backup.go|	if man.Site != id {|	if false {'                                                                        # BAK-01: another website's backup restored here
  'internal/sites/backup.go|	if !backupRe.MatchString(name) {|	if name == "" {'                                                          # BAK-01: any file name served or restored
  'internal/sites/backup.go|		if x.Kind != kind {|		if false {'                                                                     # BAK-01: daily keep prunes manual backups
  'internal/sites/backup.go|"mariadb-dump", "--single-transaction", |"mariadb-dump", '                                                    # BAK-01: inconsistent database copy
  'internal/sites/wp.go|	if err == nil && !s.IsWP() {|	if false {'                                                                # APP-01: WordPress tools run on another app
  'internal/sites/db.go|	if !s.HasDB() {|	if false {'                                                                             # APP-01: database tools on a site without database
  'internal/spec/spec.go|		if c.Server != "nginx" || c.PHP != "" {|		if c.Server != "nginx" {'                                       # APP-01: PHP on a static site
  'internal/sites/render.go|"DB_PASSWORD_FILE: /run/secrets/db_password"}|"DB_PASSWORD: x"}'                                       # APP-01: database password as an environment value
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
