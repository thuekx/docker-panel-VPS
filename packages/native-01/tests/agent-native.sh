#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# NATIVE-01 agent harness, run by harness.sh after APPLY on the throwaway CI VM: builds the agent from this commit,
# starts it with the native.json APPLY wrote, and creates real native websites (WordPress on PHP 8.4, static) through
# its socket. Checks they are served by the host's Nginx, run as their own user in their sandbox, cannot reach the
# server's private addresses, other websites or the agent, and go through backup, restore, trash and removal.
# Sourced by harness.sh (uses its ok/has helpers, IP, GW).
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
AG=/usr/local/sbin/nostek-agent
SOCK=/run/nostek-agent/agent.sock
MARK_N="# np-native-harness"
ZONE=preview.example.test
BODY=""; CODE=0

api() { # $1 method $2 path [$3 json]
  local out
  out="$(curl -s --noproxy '*' --max-time 900 --unix-socket "$SOCK" -X "$1" ${3:+-H 'Content-Type: application/json' --data "$3"} -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
wait_job() { # $1 job id → done | failed
  local st _
  for _ in $(seq 1 180); do
    api GET "/v1/jobs/$1"; st="$(grep -o '"state":"[a-z]*"' <<<"$BODY" | cut -d'"' -f4)"
    [ "$st" = running ] || { echo "$st"; return; }
    sleep 2
  done
  echo timeout
}
served() { # $1 host $2 path → status code from the host's Nginx, as the edge asks it
  curl -s -o /tmp/np-native-page -w '%{http_code}' --max-time 20 -H "Host: $1" -H 'X-Forwarded-Proto: https' "http://$GW:8088$2"
}
native_cleanup() {
  pkill -f "$AG serve" >/dev/null 2>&1
  sed -i "/$MARK_N\$/d" /etc/hosts
}
trap 'native_cleanup' EXIT

echo "INFO building the agent from $(git -C "$REPO" rev-parse --short HEAD)"
(cd "$REPO/agent" && CGO_ENABLED=0 go build -trimpath -o "$AG" ./cmd/nostek-agent) || { echo "FAIL agent build"; exit 1; }
cat > /etc/nostek-agent/config.json <<EOF
{"data": "/srv/nostek", "public_ipv4": "$IP", "panel_domain": "panel.example.test", "preview_zone": "$ZONE",
 "socket": "$SOCK", "socket_gid": 0, "allowed_uids": [0], "audit": "/var/log/nostek-agent/audit.log"}
EOF
for h in wpn-example-test sta-example-test; do echo "$IP $h.$ZONE $MARK_N" >> /etc/hosts; done
NOSTEK_AGENT_CONFIG=/etc/nostek-agent/config.json nohup "$AG" serve >> /tmp/np-native-agent.log 2>&1 &
for _ in $(seq 1 20); do api GET /v1/health; [ "$CODE" = 200 ] && break; sleep 0.5; done

api GET /v1/catalog; ok "the agent offers the native engine with PHP 8.1-8.5" 'grep -q "\"native\":{\"php\":\[\"8.1\",\"8.2\",\"8.3\",\"8.4\",\"8.5\"\]}" <<<"$BODY"'

api POST /v1/sites '{"domain":"wpn.example.test","aliases":[],"primary":"domain","server":"nginx","php":"8.4","admin_email":"owner@example.test","title":"Native WP","engine":"native"}'
JW="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"; C1="$CODE"; JWS="$(wait_job "$JW")"
OUT="$(api GET "/v1/jobs/$JW"; echo "$BODY"; tail -n 30 /tmp/np-native-agent.log)"
ok "a native WordPress is created (WP-CLI as the website user, in its sandbox)" '[ "$C1" = 202 ] && [ "$JWS" = "done" ]'
WPID=wpn-example-test; WPU=np-wpn-example-test; WPH="$WPID.$ZONE"
ok "its PHP-FPM service runs as its own user, never root or www-data" 'systemctl is-active -q nostek-php-$WPID.service && [ "$(ps -o uid= -C php-fpm8.4 | tr -d " " | sort -u)" = "$(id -u "$WPU")" ]'
ok "the host Nginx serves it (WordPress home page)" '[ "$(served "$WPH" /)" = 200 ] && grep -q "wp-content" /tmp/np-native-page'
ok "the edge entry sends its hosts to the host Nginx" 'grep -q "http://$GW:8088" /srv/nostek/edge/dynamic/site-$WPID.yml && grep -q "Host(\`$WPH\`)" /srv/nostek/edge/dynamic/site-$WPID.yml'
ok "its files belong to its user, unreadable to others" '[ "$(stat -c "%U %a" /srv/nostek/sites/$WPID/wp)" = "$WPU 750" ] && [ "$(stat -c %U /srv/nostek/sites/$WPID/wp/wp-config.php)" = "$WPU" ]'
ok "its database lives in the host MariaDB, reachable by its own user only" 'mariadb -N -e "SHOW GRANTS FOR \`np_wpn_example_test\`@localhost" | grep -q "ON \`np_wpn_example_test\`.\*" && ! mariadb -N -e "SHOW GRANTS FOR \`np_wpn_example_test\`@localhost" | grep -q "ON \*\.\*"'

api POST /v1/sites '{"domain":"sta.example.test","aliases":[],"primary":"domain","server":"nginx","php":"","admin_email":"","title":"","app":"static","engine":"native"}'
JS="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "a native static website is created without PHP or a database" '[ "$(wait_job "$JS")" = done ] && [ ! -e /etc/systemd/system/nostek-php-sta-example-test.service ] && [ "$(served sta-example-test.$ZONE /)" = 200 ]'

# the website's PHP tries to step outside; every attempt must fail (a listener on loopback stands for the panel)
python3 -m http.server 18099 --bind 127.0.0.1 >/dev/null 2>&1 & LOOP=$!
cat > "/srv/nostek/sites/$WPID/wp/np-probe.php" <<EOF
<?php
\$r = array();
\$r['uid'] = posix_geteuid();
\$r['agent_config'] = @file_get_contents('/etc/nostek-agent/config.json') !== false;
\$r['other_site'] = @file_get_contents('/srv/nostek/sites/sta-example-test/wp/index.html') !== false;
\$r['passwd_write'] = @file_put_contents('/etc/np-probe', 'x') !== false;
\$r['private_net'] = (bool) @fsockopen('$GW', 8088, \$en, \$es, 3);
\$r['loopback'] = (bool) @fsockopen('127.0.0.1', 18099, \$en, \$es, 3);
\$r['shell'] = function_exists('shell_exec') || function_exists('exec') || function_exists('proc_open');
echo json_encode(\$r);
EOF
chown "$WPU:$WPU" "/srv/nostek/sites/$WPID/wp/np-probe.php"
PROBE="$(served "$WPH" /np-probe.php; echo; cat /tmp/np-native-page)"; OUT="$PROBE"
ok "the website's PHP cannot read the agent, other websites or the system, reach private addresses or run programs" 'grep -q "\"agent_config\":false,\"other_site\":false,\"passwd_write\":false,\"private_net\":false,\"loopback\":false,\"shell\":false" <<<"$PROBE" && ! grep -q "\"uid\":0," <<<"$PROBE" && ! grep -q "\"uid\":33," <<<"$PROBE"'
PID="$(systemctl show -p MainPID --value "nostek-php-$WPID.service")"
OUT="$(nsenter -t "$PID" -m ls /srv/nostek/sites 2>&1; nsenter -t "$PID" -m ls /run/nostek-agent 2>&1)"
ok "inside its sandbox only its own website folder exists, the agent is out of reach" '[ "$(nsenter -t "$PID" -m ls /srv/nostek/sites)" = "$WPID" ] && ! nsenter -t "$PID" -m ls /run/nostek-agent >/dev/null 2>&1'
rm -f "/srv/nostek/sites/$WPID/wp/np-probe.php"; kill "$LOOP" 2>/dev/null

api GET "/v1/sites/$WPID/wp"; OUT="$BODY"
ok "WordPress Tools read plugins and themes through WP-CLI" '[ "$CODE" = 200 ] && grep -q "\"themes\":\[{" <<<"$BODY"'
api GET "/v1/sites/$WPID/db"; OUT="$BODY"
ok "the Database page lists the WordPress tables as the website's database user" '[ "$CODE" = 200 ] && grep -q "wp_posts" <<<"$BODY"'
api POST "/v1/sites/$WPID/limits" '{"php":{"memory_mb":512,"upload_mb":256,"exec_seconds":120,"max_input_vars":1000}}'; OUT="$BODY"
ok "raising PHP memory raises the service limit and restarts PHP" '[ "$CODE" = 200 ] || [ "$CODE" = 204 ]; [ "$(systemctl show -p MemoryMax --value nostek-php-$WPID.service)" = $((768*1024*1024)) ] && [ "$(served "$WPH" /)" = 200 ]'

api POST "/v1/sites/$WPID/backups"; JB="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "backup copies the database from the host MariaDB and the files" '[ "$(wait_job "$JB")" = done ] && ls /srv/nostek/sites/$WPID/backups/manual-*.tar.gz >/dev/null 2>&1'
BK="$(basename "$(ls /srv/nostek/sites/$WPID/backups/manual-*.tar.gz | head -n1)")"
mariadb -e "UPDATE np_wpn_example_test.wp_options SET option_value='Changed' WHERE option_name='blogname'"
rm -f "/srv/nostek/sites/$WPID/wp/wp-content/themes/index.php"
api POST "/v1/sites/$WPID/backups/$BK/restore" '{"what":"all"}'; JR="$(grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"; OUT="$BODY"
ok "restore brings back the database and the files, owned by the website user" '[ "$(wait_job "$JR")" = done ] && [ "$(mariadb -N -e "SELECT option_value FROM np_wpn_example_test.wp_options WHERE option_name=\"blogname\"")" = "Native WP" ] && [ "$(stat -c %U /srv/nostek/sites/$WPID/wp/wp-content/themes/index.php)" = "$WPU" ] && [ "$(served "$WPH" /)" = 200 ]'

api POST "/v1/sites/$WPID/delete"; ENTRY="$(grep -o '"trash":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
ok "delete stops it and takes it off Nginx, keeping its user and database" '[ -n "$ENTRY" ] && ! systemctl is-active -q nostek-php-$WPID.service && [ "$(served "$WPH" /)" != 200 ] && id "$WPU" >/dev/null 2>&1 && mariadb -N -e "SHOW DATABASES" | grep -qx np_wpn_example_test'
api POST "/v1/trash/$ENTRY/restore"; OUT="$BODY"
ok "restore from the trash serves it again" '[ "$CODE" = 200 ] && systemctl is-active -q nostek-php-$WPID.service && [ "$(served "$WPH" /)" = 200 ]'
api POST "/v1/sites/$WPID/delete"; ENTRY="$(grep -o '"trash":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"
api POST "/v1/trash/$ENTRY/remove"; OUT="$BODY"
ok "delete permanently removes its user and its database" '! id "$WPU" >/dev/null 2>&1 && ! mariadb -N -e "SHOW DATABASES" | grep -qx np_wpn_example_test && [ ! -e /etc/systemd/system/nostek-php-$WPID.service ]'
api POST /v1/sites/sta-example-test/delete; ENTRY="$(grep -o '"trash":"[^"]*"' <<<"$BODY" | cut -d'"' -f4)"; api POST "/v1/trash/$ENTRY/remove"
native_cleanup
