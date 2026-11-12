#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# SITE-01 harness: runs the package against a fake root with stubbed host identity and DNS, and the REAL Docker
# engine: builds the agent, starts it, creates an Apache and an Nginx WordPress site, tests isolation, trash and
# restore, then removes everything again. Needs: root, Docker with compose, git, curl, setpriv.
# The source is the commit checked out in this repository. Preview names resolve through /etc/hosts lines that
# this harness adds and removes (marker "np-site01-harness").
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../site-01-agent.sh}")"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
STUBS="$REPO/packages/panel-01/tests/stubs"
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"
IP=198.51.100.7; ZONE=preview.example.test; D=panel.example.test
MARK="# np-site01-harness"
CURL_IMG="curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69"

stop_agents() { pkill -f "nostek-agent serve" >/dev/null 2>&1; sleep 0.5; }
cleanup() {
  stop_agents
  docker ps -aq --filter label=com.docker.compose.project | while read -r id; do
    p="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id" 2>/dev/null)"
    case "$p" in np-site-*) docker rm -f "$id" >/dev/null 2>&1 ;; esac
  done
  docker network ls --format '{{.Name}}' | grep -E '^np-site-' | xargs -r docker network rm >/dev/null 2>&1
  docker network rm nostek-edge >/dev/null 2>&1
  sed -i "/$MARK\$/d" /etc/hosts
  rm -rf "$TMP_ALL"
}
trap '[ "${HARNESS_KEEP:-0}" = 1 ] || cleanup' EXIT   # HARNESS_KEEP=1 leaves a failed run in place to look at
new_env() {
  cleanup; TMP_ALL="$(mktemp -d)"
  T="$(mktemp -d "$TMP_ALL/t.XXXX")"
  export SITE01_TEST_ROOT="$T/root" SITE01_TEST_REPO="file://$REPO"
  mkdir -p "$SITE01_TEST_ROOT/var/lib/nostek/panel-01"
  echo "phase=APPLIED" > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"
  docker network create nostek-edge >/dev/null
  export PATH="$STUBS:/usr/sbin:/usr/bin:/sbin:/bin"
  export SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=$IP FAKE_DNS=$IP; unset FAKE_PUBLIC_IP || true
}
A=(--ack SITE-01 --expect-ip "$IP" --expect-host fake-vps --domain "$D" --preview-zone "$ZONE")
run() { OUT="$(bash "$SCRIPT" "$@" 2>"$T/stderr" </dev/null)"; RC=$?; }
ok() {
  TESTS=$((TESTS+1))
  if eval "$2"; then echo "PASS t$TESTS $1"; return; fi
  echo "FAIL t$TESTS $1"; echo "$OUT" | tail -n 25 | sed 's/^/    | /'; [ -n "${BODY:-}" ] && echo "    | agent: ${CODE:-} ${BODY:0:300}"
  BAD=$((BAD+1)); [ "${HARNESS_FAST_FAIL:-0}" = 1 ] && exit 1
}
has() { grep -q -- "$1" <<<"$OUT"; }
phase() { grep '^phase=' "$SITE01_TEST_ROOT/var/lib/nostek/site-01/state" 2>/dev/null | tail -n1 | cut -d= -f2; }
SOCK() { echo "$SITE01_TEST_ROOT/run/nostek-agent/agent.sock"; }
api() { # METHOD PATH [BODY] -> body; HTTP code in $CODE
  local out
  out="$(curl -s --noproxy '*' --max-time "${MAXT:-30}" --unix-socket "$(SOCK)" -X "$1" -H 'Content-Type: application/json' ${3:+--data "$3"} -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
wait_job() { # job id -> state
  local s=""
  for _ in $(seq 1 120); do
    api GET "/v1/jobs/$1"; s="$(grep -o '"state":"[a-z]*"' <<<"$BODY" | cut -d'"' -f4)"
    [ "$s" != running ] && break; sleep 3
  done
  [ "$s" = "done" ] || echo "    | job $1: ${BODY:0:600}" >&2
  echo "$s"
}
web() { # site-id host path -> http code from inside nostek-edge (as the edge proxy would call it)
  docker run --rm --network nostek-edge "$CURL_IMG" -s -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Host: $2" -H 'X-Forwarded-Proto: https' "http://np-site-$1-web-1$3"
}
site_body() { docker run --rm --network nostek-edge "$CURL_IMG" -s --max-time 20 -H "Host: $2" -H 'X-Forwarded-Proto: https' "http://np-site-$1-web-1/"; }

# --- refusals and preflight (nothing changes)
new_env; FAKE_HOSTNAME=other; run PREFLIGHT "${A[@]}"; ok "wrong host name stops" '[ $RC = 4 ] && grep -q "WRONG HOST" "$T/stderr"'
new_env; unset SUDO_USER; run PREFLIGHT "${A[@]}"; ok "root shell refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT --ack SITE-01 --expect-ip $IP --expect-host fake-vps --domain "$D" --preview-zone 'Preview.Example.test;x'; ok "bad zone refused" '[ $RC = 3 ]'
new_env; run PREFLIGHT --ack SITE-01 --expect-ip $IP --expect-host fake-vps --domain "$D" --preview-zone "$D"; ok "zone equal to panel domain refused" '[ $RC = 3 ]'
new_env; run APPLY "${A[@]}" --src-commit main; ok "commit must be a full id" '[ $RC = 3 ] && [ ! -e "$SITE01_TEST_ROOT/usr" ]'
new_env; echo phase=REVERTED > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"; run PREFLIGHT "${A[@]}"; ok "needs PANEL-01 applied" '[ $RC = 1 ] && has "PANEL-01 is not applied"'
new_env; docker network rm nostek-edge >/dev/null; run PREFLIGHT "${A[@]}"; ok "needs the edge network" '[ $RC = 1 ] && has "nostek-edge missing"'
new_env; FAKE_DNS=203.0.113.9; run PREFLIGHT "${A[@]}"; ok "missing preview wildcard only warns" '[ $RC = 0 ] && has "WARN \*.$ZONE does not point"'
new_env; run PREFLIGHT "${A[@]}"; ok "preflight passes and changes nothing" '[ $RC = 0 ] && has "RESULT SITE-01 PREFLIGHT PASS" && [ ! -e "$SITE01_TEST_ROOT/usr" ] && [ ! -e "$SITE01_TEST_ROOT/srv" ]'
new_env; run APPLY "${A[@]}" --src-commit 0000000000000000000000000000000000000000; ok "unknown commit stops before any change" '[ $RC = 1 ] && [ ! -e "$SITE01_TEST_ROOT/usr/local/sbin/nostek-agent" ] && [ -z "$(phase)" ]'

# --- install and verify
new_env; run APPLY "${A[@]}" --src-commit "$COMMIT"; ok "apply passes" '[ $RC = 0 ] && has "RESULT SITE-01 APPLY PASS version=v$(tr -d "[:space:]" < "$REPO/VERSION")" && [ "$(phase)" = APPLIED ]'
ok "socket 660 root:65532 in a 750 folder" '[ "$(stat -c "%a %u %g" "$(SOCK)")" = "660 0 65532" ] && [ "$(stat -c "%a" "$(dirname "$(SOCK)")")" = 750 ]'
ok "config 640, trash 700, no secret in config" '[ "$(stat -c %a "$SITE01_TEST_ROOT/etc/nostek-agent/config.json")" = 640 ] && [ "$(stat -c %a "$SITE01_TEST_ROOT/srv/nostek/trash")" = 700 ] && ! grep -qi password "$SITE01_TEST_ROOT/etc/nostek-agent/config.json"'
ok "systemd units written with the purge timer" 'grep -q "ExecStart=/usr/local/sbin/nostek-agent serve" "$SITE01_TEST_ROOT/etc/systemd/system/nostek-agent.service" && grep -q "OnCalendar" "$SITE01_TEST_ROOT/etc/systemd/system/nostek-agent-purge.timer"'
run VERIFY "${A[@]}"; ok "verify passes" '[ $RC = 0 ] && has "RESULT SITE-01 VERIFY PASS" && has "other users cannot call the agent" && has "refuses actions outside its list"'
api POST /v1/containers/create '{"Image":"alpine"}'; ok "raw Docker call refused" '[ "$CODE" = 404 ] || [ "$CODE" = 405 ]'
api POST /v1/sites '{"domain":"a.example.test","primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"A","image":"alpine"}'
ok "unknown field refused" '[ "$CODE" = 400 ]'
api POST /v1/sites '{"domain":"a.example.test","aliases":[],"primary":"domain","server":"apache","php":"7.4","admin_email":"o@example.test","title":"A"}'
ok "PHP outside the list refused" '[ "$CODE" = 422 ]'
api POST /v1/sites '{"domain":"a.example.test","aliases":[],"primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"A"}'
ok "no address pointing here refused" '[ "$CODE" = 422 ] && grep -q "DNS" <<<"$BODY"'

# --- real sites
printf '%s a-example-test.%s b-example-test.%s %s\n' "$IP" "$ZONE" "$ZONE" "$MARK" >> /etc/hosts
sleep 6   # the Go resolver re-reads /etc/hosts at most every 5 seconds
api POST /v1/sites '{"domain":"a.example.test","aliases":[],"primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"Situs A"}'
JA="$(grep -o '"job":"[a-z0-9]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "apache site accepted" '[ "$CODE" = 202 ] && [ -n "$JA" ]'
api POST /v1/sites '{"domain":"b.example.test","aliases":[],"primary":"www","server":"nginx","php":"8.3","admin_email":"o@example.test","title":"Situs B"}'
ok "second change while one runs is refused (busy)" '[ "$CODE" = 409 ]'
ok "apache site created" '[ "$(wait_job "$JA")" = done ]'
api POST /v1/sites '{"domain":"b.example.test","aliases":[],"primary":"www","server":"nginx","php":"8.3","admin_email":"o@example.test","title":"Situs B"}'
JB="$(grep -o '"job":"[a-z0-9]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "nginx site created" '[ "$CODE" = 202 ] && [ "$(wait_job "$JB")" = done ]'
HOME_A="$(site_body a-example-test "a-example-test.$ZONE")"; ok "apache WordPress answers with its title" 'grep -q "<title>Situs A" <<<"$HOME_A"'
ok "nginx WordPress answers, wp-config.php hidden" '[ "$(web b-example-test b-example-test.$ZONE /)" = 200 ] && [ "$(web b-example-test b-example-test.$ZONE /wp-config.php)" = 403 ]'
api POST "/v1/jobs/$JA/secret"; PW="$(grep -o '"password":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "admin password handed out once" '[ ${#PW} = 24 ] && { api POST "/v1/jobs/$JA/secret"; [ "$CODE" = 410 ]; }'
ok "password not stored on disk or in the audit log" '! grep -rqF "$PW" "$SITE01_TEST_ROOT/srv/nostek/sites" "$SITE01_TEST_ROOT/var/log/nostek-agent" 2>/dev/null'
B_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' np-site-b-example-test-db-1)"
ok "site A cannot reach site B's database" '! docker exec np-site-a-example-test-web-1 bash -c "timeout 3 bash -c \"</dev/tcp/$B_IP/3306\"" 2>/dev/null && docker exec np-site-a-example-test-web-1 bash -c "timeout 3 bash -c \"</dev/tcp/db/3306\""'
ok "database is not on the edge network" '! docker inspect -f "{{range \$k,\$v := .NetworkSettings.Networks}}{{\$k}} {{end}}" np-site-a-example-test-db-1 | grep -q nostek-edge'
ok "no site container publishes ports" '[ -z "$(docker ps --filter name=np-site- --format "{{.Ports}}" | grep "0.0.0.0")" ]'
api GET /v1/sites; ok "both sites listed as running" '[ "$(grep -o "\"status\":\"running\"" <<<"$BODY" | wc -l)" = 2 ]'
api POST /v1/sites/a-example-test/stop; api GET /v1/sites/a-example-test; ok "stop shows stopped" 'grep -q "\"status\":\"stopped\"" <<<"$BODY"'
api POST /v1/sites/a-example-test/start; ok "start brings it back" '[ "$CODE" = 200 ] && [ "$(web a-example-test a-example-test.$ZONE /)" = 200 ]'

# --- STACK-01: containers with live usage; restart one container
stackcheck() { python3 -c 'import json,sys; s=json.load(sys.stdin); c=s["containers"]; assert s["project"]=="np-site-b-example-test", s; assert [x["service"] for x in c]==["web","php","db"], c; assert all(x["state"]=="running" and x["mem"]>0 and x["cpu"]>=0 and x["started"]>0 for x in c), c; assert c[2]["health"]=="healthy", c; assert "@" not in "".join(x["image"] for x in c)' <<<"$BODY"; }
api GET /v1/sites/b-example-test/stack; ok "stack lists web, php, db with live CPU and memory" '[ "$CODE" = 200 ] && stackcheck'
PHP_START="$(docker inspect -f "{{.State.StartedAt}}" np-site-b-example-test-php-1)"
api POST /v1/sites/b-example-test/services/php/restart; ok "restart one container (php) only" '[ "$CODE" = 200 ] && [ "$(docker inspect -f "{{.State.StartedAt}}" np-site-b-example-test-php-1)" != "$PHP_START" ] && [ "$(web b-example-test b-example-test.$ZONE /)" = 200 ]'
api POST /v1/sites/a-example-test/services/php/restart; ok "apache site has no php container to restart" '[ "$CODE" = 400 ]'

# --- SITE-02 file manager and database (real files, real MariaDB)
raw() { # METHOD PATH FILE|- -> body in $BODY (or saved to $RAWOUT), HTTP code in $CODE
  local out
  out="$(curl -s --noproxy '*' --max-time 120 --unix-socket "$(SOCK)" -X "$1" -H 'Content-Type: application/octet-stream' ${3:+--data-binary "@$3"} ${RAWOUT:+-o "$RAWOUT"} -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
site_get() { docker run --rm --network nostek-edge "$CURL_IMG" -s --max-time 20 -H "Host: $2" -H 'X-Forwarded-Proto: https' "http://np-site-$1-web-1$3"; }
WPB="$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/wp"
printf 'hello from the file manager\n' > "$T/hello.txt"
raw PUT '/v1/sites/b-example-test/files/content?path=/hello.txt' "$T/hello.txt"
ok "upload a file: www-data owns it and the website serves it" '[ "$CODE" = 200 ] && [ "$(stat -c "%u:%g %a" "$WPB/hello.txt")" = "33:33 644" ] && [ "$(site_get b-example-test b-example-test.$ZONE /hello.txt)" = "hello from the file manager" ]'
api GET '/v1/sites/b-example-test/files?path=/'; ok "folder list shows WordPress" '[ "$CODE" = 200 ] && grep -q "\"name\":\"wp-config.php\"" <<<"$BODY" && grep -q "\"name\":\"wp-content\",\"type\":\"dir\"" <<<"$BODY"'
ln -s ../secrets/db_password "$WPB/steal" && ln -s /etc/shadow "$WPB/host"
DBPW="$(cat "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/secrets/db_password")"
api GET '/v1/sites/b-example-test/files/content?path=/steal'; C1="$CODE"; B1="$BODY"; api GET '/v1/sites/b-example-test/files/content?path=/host'
ok "links to the secret or the host are not followed" '[ "$C1" = 400 ] && [ "$CODE" = 400 ] && ! grep -qF "$DBPW" <<<"$B1$BODY"'
rm -f "$WPB/steal" "$WPB/host"
( cd "$T" && mkdir -p zipsrc/kit && printf 'unzipped ok\n' > zipsrc/kit/note.txt && cd zipsrc && python3 -c 'import zipfile;z=zipfile.ZipFile("../kit.zip","w");z.write("kit/note.txt");z.close()' )
raw PUT '/v1/sites/b-example-test/files/content?path=/kit.zip' "$T/kit.zip"; api POST /v1/sites/b-example-test/files/unzip '{"path":"/kit.zip"}'
ok "extract a zip in place" '[ "$CODE" = 200 ] && [ "$(site_get b-example-test b-example-test.$ZONE /kit/note.txt)" = "unzipped ok" ] && [ "$(stat -c %u "$WPB/kit/note.txt")" = 33 ]'
api POST /v1/sites/b-example-test/files/delete '{"path":"/kit"}'; FT="$(grep -o '"entry":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "delete moves to the file trash" '[ "$CODE" = 200 ] && [ ! -e "$WPB/kit" ] && [ "$(web b-example-test b-example-test.$ZONE /kit/note.txt)" = 404 ]'
api POST "/v1/sites/b-example-test/files/trash/$FT/restore"; ok "restore from the file trash" '[ "$CODE" = 200 ] && [ "$(site_get b-example-test b-example-test.$ZONE /kit/note.txt)" = "unzipped ok" ]'
dbcheck() { python3 -c 'import json,sys; d=json.load(sys.stdin); n=[t["name"] for t in d["tables"]]; assert "wp_options" in n and "wp_posts" in n, n; assert "MariaDB" in d["version"] and d["size"]>0, d' <<<"$BODY"; }
api GET /v1/sites/b-example-test/db; ok "database overview lists the WordPress tables" '[ "$CODE" = 200 ] && dbcheck'
api POST /v1/sites/b-example-test/db/query '{"sql":"SELECT option_value FROM wp_options WHERE option_name = '"'"'blogname'"'"'"}'
ok "SQL console reads the site title" '[ "$CODE" = 200 ] && grep -q "\"Situs B\"" <<<"$BODY"'
api POST /v1/sites/b-example-test/db/query '{"sql":"SELEC 1"}'; ok "SQL error is shown, not hidden" '[ "$CODE" = 400 ] && grep -q "ERROR 1064" <<<"$BODY"'
RAWOUT="$T/export.sql.gz" raw GET /v1/sites/b-example-test/db/export
ok "export is a gzip dump with the tables" '[ "$CODE" = 200 ] && gzip -dc "$T/export.sql.gz" > "$T/export.sql" && grep -q "CREATE TABLE \`wp_options\`" "$T/export.sql"'
printf "UPDATE wp_options SET option_value = 'Diimpor' WHERE option_name = 'blogname';\n" | gzip > "$T/imp.sql.gz"
raw PUT /v1/sites/b-example-test/db/import "$T/imp.sql.gz"; BK="$(grep -o '"backup":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "import changes the site and keeps a copy first" '[ "$CODE" = 200 ] && [ -n "$BK" ] && grep -q "<title>Diimpor" <<<"$(site_body b-example-test "b-example-test.$ZONE")"'
RAWOUT="$T/back.sql.gz" raw GET "/v1/sites/b-example-test/db/backups/$BK"; raw PUT /v1/sites/b-example-test/db/import "$T/back.sql.gz"
ok "importing the kept copy undoes the import" '[ "$CODE" = 200 ] && grep -q "<title>Situs B" <<<"$(site_body b-example-test "b-example-test.$ZONE")"'
ok "database password never in the audit log or an answer" '! grep -qF "$DBPW" "$SITE01_TEST_ROOT/var/log/nostek-agent/"* && ! grep -qF "$DBPW" "$T/export.sql"'

# --- WP-01 WordPress Tools (real WP-CLI in the cli container; no internet needed for these)
site_head() { docker run --rm --network nostek-edge "$CURL_IMG" -s -o /dev/null -D - --max-time 20 -H "Host: $2" -H 'X-Forwarded-Proto: https' "http://np-site-$1-web-1$3"; }
wpcheck() { python3 -c 'import json,sys; w=json.load(sys.stdin); assert w["version"] and w["title"]=="Situs B", w; assert w["admins"] and w["file_edit_off"] and w["ssl_admin"], w; n=[p["name"] for p in w["plugins"]]; assert "akismet" in n and "nostek-panel" in n, n; assert any(t["status"]=="active" for t in w["themes"]), w["themes"]' <<<"$BODY"; }
MAXT=240 api GET /v1/sites/b-example-test/wp; ok "WordPress overview: version, admins, plugins, themes" '[ "$CODE" = 200 ] && wpcheck'
ok "cli container now has outbound internet, compose kept in sync" 'grep -q "networks: \[back, out\]" "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/compose.yml" && [ "$(stat -c %u "$WPB/wp-content/mu-plugins/nostek-panel.php")" = 33 ]'
MAXT=120 api POST /v1/sites/b-example-test/wp/login; LINK="$(grep -o '"url":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"; LPATH="/${LINK#https://*/}"
H1="$(site_head b-example-test b-example-test.$ZONE "$LPATH")"; H2="$(site_head b-example-test b-example-test.$ZONE "$LPATH")"
ok "one-click login link signs in once, then is spent" 'grep -q "^HTTP/1.1 302" <<<"$H1" && grep -qi "^location: https://b-example-test.$ZONE/wp-admin/" <<<"$H1" && grep -qi "set-cookie: wordpress_logged_in" <<<"$H1" && grep -q "^HTTP/1.1 403" <<<"$H2"'
ok "login link not in the audit log" '! grep -rqF "${LINK##*=}" "$SITE01_TEST_ROOT/var/log/nostek-agent"'
MAXT=120 api POST /v1/sites/b-example-test/wp/settings '{"key":"maintenance","on":true}'
ok "maintenance mode shows visitors a 503 page" '[ "$CODE" = 200 ] && [ "$(web b-example-test b-example-test.$ZONE /)" = 503 ]'
MAXT=120 api POST /v1/sites/b-example-test/wp/settings '{"key":"maintenance","on":false}'; ok "maintenance off serves the site again" '[ "$(web b-example-test b-example-test.$ZONE /)" = 200 ]'
MAXT=120 api POST /v1/sites/b-example-test/wp/settings '{"key":"debug","on":true}'; C1="$CODE"
MAXT=120 api POST /v1/sites/b-example-test/wp/settings '{"key":"system_cron","on":true}'
ok "debug and system cron switch wp-config.php" '[ "$C1" = 200 ] && [ "$CODE" = 200 ] && grep -q "define( '"'"'WP_DEBUG'"'"', true )" "$WPB/wp-config.php" && grep -q "/dev/stderr" "$WPB/wp-config.php" && grep -q "DISABLE_WP_CRON'"'"', true" "$WPB/wp-config.php" && grep -q "\"system_cron\": *true" "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/site.json" && [ "$(web b-example-test b-example-test.$ZONE /)" = 200 ]'
ok "system cron runs wp-cron.php in the php container" 'docker compose -p np-site-b-example-test -f "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/compose.yml" exec -T -u 33:33 php php /var/www/html/wp-cron.php'
MAXT=120 api POST /v1/sites/b-example-test/wp/settings '{"key":"indexing","on":false}'
api POST /v1/sites/b-example-test/db/query '{"sql":"SELECT option_value FROM wp_options WHERE option_name = '"'"'blog_public'"'"'"}'; ok "search indexing off" 'grep -q "\"0\"" <<<"$BODY"'
MAXT=240 api POST /v1/sites/b-example-test/wp/plugins '{"do":"activate","names":["akismet"]}'; C1="$CODE"
MAXT=240 api GET /v1/sites/b-example-test/wp; ok "activate a plugin" '[ "$C1" = 200 ] && python3 -c "import json,sys; w=json.load(sys.stdin); assert [p for p in w[\"plugins\"] if p[\"name\"]==\"akismet\"][0][\"status\"]==\"active\"" <<<"$BODY"'
MAXT=240 api POST /v1/sites/b-example-test/wp/plugins '{"do":"delete","names":["hello"]}'
ok "delete a plugin" '[ "$CODE" = 200 ] && [ ! -e "$WPB/wp-content/plugins/hello.php" ]'
MAXT=240 api POST /v1/sites/b-example-test/wp/plugins '{"do":"activate","names":["nosuchplugin"]}'; ok "unknown plugin is a clear error" '[ "$CODE" = 400 ] && grep -qi "nosuchplugin" <<<"$BODY"'
MAXT=240 api POST /v1/sites/b-example-test/wp/cli '{"line":"wp option get blogname"}'; ok "WP-CLI console runs a command" '[ "$CODE" = 200 ] && grep -q "\"output\":\"Situs B\"" <<<"$BODY" && grep -q "\"code\":0" <<<"$BODY"'
MAXT=240 api POST /v1/sites/b-example-test/wp/cli '{"line":"option get blogname; id > /tmp/pwn $(id)"}'; ok "console has no shell" '[ "$CODE" = 200 ] && ! grep -q "uid=" <<<"$BODY"'
api POST /v1/sites/b-example-test/wp/cli '{"line":"shell"}'; ok "interactive console commands refused" '[ "$CODE" = 400 ]'

# --- BAK-01 website backup and restore (real mariadb-dump, real file swap)
BKD="$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/backups"
bakname() { python3 -c 'import json,sys; b=json.load(sys.stdin)["backups"]; k=[x["name"] for x in b if x["kind"]==sys.argv[1]]; print(k[0] if k else "")' "$1" <<<"$BODY"; }
api POST /v1/sites/b-example-test/backups; JBK="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "backup made" '[ "$CODE" = 202 ] && [ "$(wait_job "$JBK")" = done ]'
api GET /v1/sites/b-example-test/backups; BKN="$(bakname manual)"
ok "backup archive: manifest first, database dump, website files, private" '[ -n "$BKN" ] && [ "$(stat -c "%a %u" "$BKD/$BKN")" = "600 0" ] && [ "$(tar -tzf "$BKD/$BKN" | head -n1)" = manifest.json ] && tar -xzOf "$BKD/$BKN" database.sql | grep -q "CREATE TABLE \`wp_options\`" && tar -tzf "$BKD/$BKN" | grep -qx "wp/wp-config.php" && ! ls "$BKD" | grep -qv "\.tar\.gz$"'
ok "database password not in the archive listing or the audit log" '! tar -xzOf "$BKD/$BKN" manifest.json site.json | grep -qF "$DBPW" && ! grep -qF "$DBPW" "$SITE01_TEST_ROOT/var/log/nostek-agent/"*'
RAWOUT="$T/site.tar.gz" raw GET "/v1/sites/b-example-test/backups/$BKN"; ok "download is the same archive" '[ "$CODE" = 200 ] && cmp -s "$T/site.tar.gz" "$BKD/$BKN"'
OWN="$(stat -c "%u:%g %a" "$WPB/wp-config.php")"
api POST /v1/sites/b-example-test/db/query '{"sql":"UPDATE wp_options SET option_value = '"'"'Rusak'"'"' WHERE option_name = '"'"'blogname'"'"'; CREATE TABLE np_after (id int)"}'
printf 'x' > "$WPB/rusak.php"; printf '<?php // changed' > "$WPB/index.php"
api POST "/v1/sites/b-example-test/backups/$BKN/restore" '{"what":"all"}'; JR="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "restore everything" '[ "$CODE" = 202 ] && [ "$(wait_job "$JR")" = done ]'
ok "website, database and files back as in the backup" '[ "$(web b-example-test b-example-test.$ZONE /)" = 200 ] && grep -q "<title>Situs B" <<<"$(site_body b-example-test "b-example-test.$ZONE")" && [ ! -e "$WPB/rusak.php" ] && ! grep -q changed "$WPB/index.php" && [ "$(stat -c "%u:%g %a" "$WPB/wp-config.php")" = "$OWN" ]'
api POST /v1/sites/b-example-test/db/query '{"sql":"SHOW TABLES LIKE '"'"'np_after'"'"'"}'; ok "tables made after the backup are gone" '[ "$CODE" = 200 ] && ! grep -q np_after <<<"$BODY"'
api GET /v1/sites/b-example-test/backups; SAFE="$(bakname before-restore)"
ok "safety copy of the state before restore kept" '[ -n "$SAFE" ] && tar -tzf "$BKD/$SAFE" | grep -qx "wp/rusak.php" && ! ls "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test" | grep -q "^wp\."'
( cd "$T" && mkdir -p evil/wp && printf '{"format":"nostek-site-backup/1","site":"b-example-test","complete":true}' > evil/manifest.json && printf -- '-- x\n' > evil/database.sql && printf 'x' > evil/wp/a.php \
  && python3 -c 'import tarfile;t=tarfile.open("evil.tar.gz","w:gz");[t.add("evil/"+n,arcname=n) for n in ("manifest.json","database.sql","wp/a.php")];i=tarfile.TarInfo("wp/../../../escape.php");i.size=1;import io;t.addfile(i,io.BytesIO(b"x"));t.close()' )
cp "$T/evil.tar.gz" "$BKD/manual-20200101-000000.tar.gz"
api POST /v1/sites/b-example-test/backups/manual-20200101-000000.tar.gz/restore '{"what":"files"}'; JE="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
SE="$(wait_job "$JE" 2>/dev/null)"; api GET "/v1/jobs/$JE"
ok "hostile archive refused, website untouched" '[ "$SE" = failed ] && grep -q "outside the website" <<<"$BODY" && [ ! -e "$SITE01_TEST_ROOT/srv/nostek/escape.php" ] && [ ! -e "$SITE01_TEST_ROOT/srv/nostek/sites/escape.php" ] && [ "$(web b-example-test b-example-test.$ZONE /)" = 200 ]'
api POST /v1/sites/b-example-test/backups/schedule '{"daily":true,"keep":3}'
ok "daily backup switched on" '[ "$CODE" = 200 ] && grep -q "\"backup_daily\": *true" "$SITE01_TEST_ROOT/srv/nostek/sites/b-example-test/site.json"'
api POST /v1/sites/b-example-test/backups/manual-20200101-000000.tar.gz/delete; ok "delete a backup" '[ "$CODE" = 200 ] && [ ! -e "$BKD/manual-20200101-000000.tar.gz" ]'
api GET "/v1/sites/b-example-test/backups/..%2Fsite.json"; ok "backup names outside the list refused" '[ "$CODE" = 400 ]'

# --- update in place keeps sites running; delete to trash and restore
CIDS="$(docker ps -q --filter name=np-site- | sort | tr '\n' ' ')"
run APPLY "${A[@]}" --src-commit "$COMMIT"; ok "apply again (update) passes, websites untouched" '[ $RC = 0 ] && has "already built" && [ "$(docker ps -q --filter name=np-site- | sort | tr "\n" " ")" = "$CIDS" ]'
api POST /v1/sites/b-example-test/delete; ENTRY="$(grep -o '"trash":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "delete moves the site to the trash" '[ "$CODE" = 200 ] && [ -d "$SITE01_TEST_ROOT/srv/nostek/trash/$ENTRY/db" ] && [ -z "$(docker ps -q --filter name=np-site-b-example-test)" ]'
api POST /v1/sites '{"domain":"b.example.test","aliases":[],"primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"B2"}'
ok "trashed domain stays reserved" '[ "$CODE" = 422 ]'
api POST "/v1/trash/$ENTRY/restore"; ok "restore brings the site back with its data" '[ "$CODE" = 200 ] && [ "$(web b-example-test b-example-test.$ZONE /)" = 200 ]'

# --- revert keeps websites running
run REVERT "${A[@]}"; ok "revert stops the agent, websites keep running" '[ $RC = 0 ] && [ "$(phase)" = REVERTED ] && [ ! -e "$SITE01_TEST_ROOT/usr/local/sbin/nostek-agent" ] && [ "$(web a-example-test a-example-test.$ZONE /)" = 200 ] && has "sites_kept=2"'
run VERIFY "${A[@]}"; ok "verify fails after revert" '[ $RC = 1 ]'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$(docker ps -aq --filter name=np-site- | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT SITE-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT SITE-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
