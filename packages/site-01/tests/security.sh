#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# SEC-01 harness (real Docker and a real Traefik edge, run as the edge's user 65532 like in production): a static
# website gets an IP block, a password-protected folder and hotlink protection, each checked through the edge, then
# removed again. Names resolve through /etc/hosts lines that this harness adds (marker "np-sec01-harness"). The
# passwords and their bcrypt hashes below are throwaway test values.
# Needs: root, Docker with compose, git, curl, python3.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "$HERE/../site-01-agent.sh")"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
STUBS="$REPO/packages/panel-01/tests/stubs"
GO="$(command -v go || true)"   # builds the WAF binary (wave 3b); found before PATH is narrowed to the stubs
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"
IP=198.51.100.7; ZONE=preview.example.test; D=panel.example.test
MARK="# np-sec01-harness"
EDGEIMG="$(grep -o 'traefik:v[^ ]*@sha256:[0-9a-f]*' "$REPO/deploy/core/compose.yml" | head -n1)"
S=s.example.test; SID="s-example-test"
PASS1=harness-pass-1; HASH1='$2a$10$T4KbINOhRSApr6B7bAO5OuLdBPSEdrkeKV7eZfPUnFGyz17J4DR36'
PASS2=harness-pass-2; HASH2='$2a$10$a9YnhWbjDSLBQy4eOYlmyeF6rHnA3wOBuj01mrWIa4Yhx94PukCqe'

stop_agents() { pkill -f "nostek-agent serve" >/dev/null 2>&1; sleep 0.5; }
cleanup() {
  stop_agents
  docker ps -aq --filter label=com.docker.compose.project | while read -r id; do
    p="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id" 2>/dev/null)"
    case "$p" in np-site-*) docker rm -f "$id" >/dev/null 2>&1 ;; esac
  done
  docker rm -f np-sec-edge nostek-core-waf-1 >/dev/null 2>&1
  docker network ls --format '{{.Name}}' | grep -E '^np-site-' | xargs -r docker network rm >/dev/null 2>&1
  docker network rm nostek-edge >/dev/null 2>&1
  sed -i "/$MARK\$/d" /etc/hosts
  rm -rf "$TMP_ALL"
}
trap '[ "${HARNESS_KEEP:-0}" = 1 ] || cleanup' EXIT
ok() {
  TESTS=$((TESTS+1))
  if eval "$2"; then echo "PASS t$TESTS $1"; return; fi
  echo "FAIL t$TESTS $1"; [ -n "${OUT:-}" ] && echo "$OUT" | tail -n 15 | sed 's/^/    | /'; [ -n "${BODY:-}" ] && echo "    | agent: ${CODE:-} ${BODY:0:600}"
  BAD=$((BAD+1)); [ "${HARNESS_FAST_FAIL:-0}" = 1 ] && exit 1
}
SOCK() { echo "$SITE01_TEST_ROOT/run/nostek-agent/agent.sock"; }
api() {
  local out
  out="$(curl -s --noproxy '*' --max-time "${MAXT:-60}" --unix-socket "$(SOCK)" -X "$1" -H 'Content-Type: application/json' ${3:+--data "$3"} -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
raw() {
  local out
  out="$(curl -s --noproxy '*' --max-time 60 --unix-socket "$(SOCK)" -X "$1" -H 'Content-Type: application/octet-stream' --data-binary "@$3" -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
jobid() { grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4; }
wait_job() {
  local s=""
  for _ in $(seq 1 "${2:-200}"); do
    api GET "/v1/jobs/$1"; s="$(grep -o '"state":"[a-z]*"' <<<"$BODY" | cut -d'"' -f4)"
    [ "$s" != running ] && break; sleep 3
  done
  echo "$s"
}
change() { api POST "/v1/sites/$SID/security" "$1"; J="$(jobid)"; }
applied() { change "$1"; [ "$CODE" = 202 ] && [ "$(wait_job "$J")" = "done" ]; }
# visit NAME PATH [curl args...] -> "code redirect" through the edge (its default certificate: -k)
visit() { local h="$1" p="$2"; shift 2; curl -sk --noproxy '*' --max-time 20 --resolve "$h:443:127.0.0.1" -o /dev/null -w '%{http_code} %{redirect_url}' "$@" "https://$h$p"; }
# edge NAME PATH WANT [curl args...]: the edge may need a few seconds to see a change
edge() { local h="$1" p="$2" w="$3" got=""; shift 3; for _ in $(seq 1 20); do got="$(visit "$h" "$p" "$@")"; [ "$got" = "$w" ] && return 0; sleep 1; done; echo "    | $h$p $* -> $got (want $w)"; return 1; }

[ -n "$EDGEIMG" ] || { echo "RESULT SEC-01 HARNESS FAIL edge image not found"; exit 1; }
cleanup; TMP_ALL="$(mktemp -d)"; T="$(mktemp -d "$TMP_ALL/t.XXXX")"; chmod 0755 "$TMP_ALL" "$T"
export SITE01_TEST_ROOT="$T/root" SITE01_TEST_REPO="file://$REPO"
mkdir -p "$SITE01_TEST_ROOT/var/lib/nostek/panel-01" "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic" "$SITE01_TEST_ROOT/srv/nostek/edge/logs"; echo "phase=APPLIED" > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"
chmod 0755 "$SITE01_TEST_ROOT" "$SITE01_TEST_ROOT/srv" "$SITE01_TEST_ROOT/srv/nostek" "$SITE01_TEST_ROOT/srv/nostek/edge" "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic"
chown 65532:65532 "$SITE01_TEST_ROOT/srv/nostek/edge/logs"; chmod 700 "$SITE01_TEST_ROOT/srv/nostek/edge/logs"
# the access log flags exactly as the core stack sets them (SEC-01 wave 3)
mapfile -t LOGFLAGS < <(grep -oE -- '--accessLog[^ ]*' "$REPO/deploy/core/compose.yml")
docker network create nostek-edge >/dev/null
GW="$(docker network inspect nostek-edge -f '{{(index .IPAM.Config 0).Gateway}}')"
docker run -d --name np-sec-edge --user 65532:65532 --group-add "$(stat -c %g /var/run/docker.sock)" --network nostek-edge -p 127.0.0.1:443:8443 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro -v "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic:/dynamic:ro" -v "$SITE01_TEST_ROOT/srv/nostek/edge/logs:/logs" "$EDGEIMG" \
  "${LOGFLAGS[@]}" \
  --global.checkNewVersion=false --global.sendAnonymousUsage=false --log.level=INFO \
  --entryPoints.websecure.address=:8443 --entryPoints.websecure.http.tls=true \
  --providers.docker=true --providers.docker.exposedByDefault=false --providers.docker.network=nostek-edge \
  --providers.file.directory=/dynamic --providers.file.watch=true \
  --certificatesResolvers.le.acme.storage=/tmp/acme.json --certificatesResolvers.le.acme.caServer=https://127.0.0.1:9/directory \
  --certificatesResolvers.le.acme.tlsChallenge=true >/dev/null
printf '%s %s %s\n%s www.%s %s\n' "$IP" "$S" "$MARK" "$IP" "$S" "$MARK" >> /etc/hosts
export PATH="$STUBS:/usr/sbin:/usr/bin:/sbin:/bin" SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=$IP FAKE_DNS=$IP
OUT="$(bash "$SCRIPT" APPLY --ack SITE-01 --expect-ip "$IP" --expect-host fake-vps --domain "$D" --preview-zone "$ZONE" --src-commit "$COMMIT" 2>&1 </dev/null)"; RC=$?
ok "agent installed" '[ $RC = 0 ] && grep -q "RESULT SITE-01 APPLY PASS" <<<"$OUT"'
OUT=""

api POST /v1/sites "{\"app\":\"static\",\"domain\":\"$S\",\"aliases\":[],\"primary\":\"domain\",\"server\":\"nginx\",\"php\":\"\",\"admin_email\":\"\",\"title\":\"Situs S\"}"; J="$(jobid)"
ok "static website made on its domain" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && edge "$S" / "200 " && edge "www.$S" /a "301 https://$S/a"'
printf '\x89PNG\r\n' > "$T/logo.png"; printf 'rahasia\n' > "$T/secret.txt"; printf 'teks\n' > "$T/note.txt"
api POST "/v1/sites/$SID/files/mkdir" '{"path":"/admin"}'; C0="$CODE"
raw PUT "/v1/sites/$SID/files/content?path=/logo.png" "$T/logo.png"; C1="$CODE"
raw PUT "/v1/sites/$SID/files/content?path=/admin/secret.txt" "$T/secret.txt"; C2="$CODE"
raw PUT "/v1/sites/$SID/files/content?path=/adminx.txt" "$T/note.txt"
ok "test files uploaded and served" '[ "$C0" = 200 ] && [ "$C1" = 200 ] && [ "$C2" = 200 ] && edge "$S" /logo.png "200 " && edge "$S" /admin/secret.txt "200 "'
api GET "/v1/sites/$SID/security"
ok "Security view: nothing set" '[ "$CODE" = 200 ] && grep -q "\"blocked\":\[\]" <<<"$BODY" && grep -q "\"hotlink\":null" <<<"$BODY"'

# --- IP Blocker: the edge sees this harness's visits from the edge network's gateway
ok "blocking this server's own address is refused" 'change "{\"op\":\"block-add\",\"ip\":\"$IP\"}"; [ "$CODE" = 400 ]'
ok "a network wider than /8 is refused" 'change "{\"op\":\"block-add\",\"ip\":\"0.0.0.0/0\"}"; [ "$CODE" = 400 ]'
ok "another address blocked: the visit still works" 'applied "{\"op\":\"block-add\",\"ip\":\"192.0.2.0/24\"}" && edge "$S" / "200 "'
ok "the visitor's address blocked: 403 on every name" 'applied "{\"op\":\"block-add\",\"ip\":\"$GW\"}" && edge "$S" / "403 " && edge "www.$S" / "403 " && edge "$S" /logo.png "403 "'
ok "unblocked: served again" 'applied "{\"op\":\"block-remove\",\"ip\":\"$GW\"}" && edge "$S" / "200 " && edge "www.$S" /a "301 https://$S/a"'
ok "the other block removed" 'applied "{\"op\":\"block-remove\",\"ip\":\"192.0.2.0/24\"}" && api GET "/v1/sites/$SID/security" && grep -q "\"blocked\":\[\]" <<<"$BODY"'

# --- password-protected folder
ok "folder /admin protected for ana" 'applied "{\"op\":\"protect-set\",\"path\":\"/admin\",\"user\":\"ana\",\"hash\":\"$HASH1\"}" && edge "$S" /admin/secret.txt "401 " && edge "$S" /admin "401 "'
ok "ana with her password gets in, a wrong password does not" 'edge "$S" /admin/secret.txt "200 " -u "ana:$PASS1" && [ "$(curl -sk --noproxy "*" --resolve "$S:443:127.0.0.1" -u "ana:$PASS1" "https://$S/admin/secret.txt")" = rahasia ] && edge "$S" /admin/secret.txt "401 " -u "ana:$PASS2"'
ok "other paths stay open (/adminx.txt, /)" 'edge "$S" /adminx.txt "200 " && edge "$S" / "200 "'
AF="$(ls "$SITE01_TEST_ROOT"/srv/nostek/edge/dynamic/auth/site-$SID-*.htpasswd 2>/dev/null | head -n1)"
ok "password file: only the edge user can read it" '[ -n "$AF" ] && [ "$(stat -c "%a %u" "$AF")" = "600 65532" ]'
ok "a second user is read by the edge at once" 'applied "{\"op\":\"protect-set\",\"path\":\"/admin/\",\"user\":\"budi\",\"hash\":\"$HASH2\"}" && edge "$S" /admin/secret.txt "200 " -u "budi:$PASS2" && edge "$S" /admin/secret.txt "200 " -u "ana:$PASS1"'
ok "a new password for ana replaces the old one" 'applied "{\"op\":\"protect-set\",\"path\":\"/admin\",\"user\":\"ana\",\"hash\":\"$HASH2\"}" && edge "$S" /admin/secret.txt "401 " -u "ana:$PASS1" && edge "$S" /admin/secret.txt "200 " -u "ana:$PASS2"'
ok "user budi removed" 'applied "{\"op\":\"protect-user-remove\",\"path\":\"/admin\",\"user\":\"budi\"}" && edge "$S" /admin/secret.txt "401 " -u "budi:$PASS2"'
ok "the last user cannot be removed" 'change "{\"op\":\"protect-user-remove\",\"path\":\"/admin\",\"user\":\"ana\"}"; [ "$CODE" = 400 ]'
ok "/.well-known cannot be protected; a hash that is not bcrypt is refused" 'change "{\"op\":\"protect-set\",\"path\":\"/.well-known\",\"user\":\"x\",\"hash\":\"$HASH1\"}"; C=$CODE; change "{\"op\":\"protect-set\",\"path\":\"/b\",\"user\":\"x\",\"hash\":\"plain\"}"; [ "$C" = 400 ] && [ "$CODE" = 400 ]'
ok "no hash in site.json or the audit log" '! grep -q "\$2a\$" "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/site.json" && ! grep -q "\$2a\$" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log" && grep -q "\"detail\":\"protect-set /admin user ana job" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log"'
ok "protection removed: open again, password file gone" 'applied "{\"op\":\"protect-remove\",\"path\":\"/admin\"}" && edge "$S" /admin/secret.txt "200 " && [ ! -e "$AF" ]'

# --- hotlink protection
ok "hotlink protection on" 'applied "{\"op\":\"hotlink-on\",\"allow\":[]}"'
ok "an image embedded by another site: 403" 'edge "$S" /logo.png "403 " -e https://evil.example.test/page'
ok "the website itself, no referrer, and non-media files still work" 'edge "$S" /logo.png "200 " -e "https://$S/" && edge "$S" /logo.png "200 " -e "https://www.$S/x" && edge "$S" /logo.png "200 " && edge "$S" /adminx.txt "200 " -e https://evil.example.test/'
ok "a look-alike name is not allowed" 'edge "$S" /logo.png "403 " -e "https://$S.evil.example.test/"'
ok "an allowed site may embed" 'applied "{\"op\":\"hotlink-on\",\"allow\":[\"evil.example.test\"]}" && edge "$S" /logo.png "200 " -e https://evil.example.test/page'
ok "hotlink protection off" 'applied "{\"op\":\"hotlink-off\"}" && edge "$S" /logo.png "200 " -e https://other.example.test/'
change '{"op":"hotlink-off"}'
ok "turning it off twice changes nothing" '[ "$CODE" = 200 ] && [ -z "$J" ]'
# --- SEC-01 wave 2: error pages and MIME types in the website's own Nginx
hdr() { curl -sk --noproxy '*' --max-time 20 --resolve "$S:443:127.0.0.1" -o /dev/null -D - "https://$S$1" | tr -d '\r' | grep -i "^content-type:" | cut -d' ' -f2- | head -n1; }
body() { curl -sk --noproxy '*' --max-time 20 --resolve "$S:443:127.0.0.1" "https://$S$1"; }
ok "404 page on: a missing file gets the starter page with code 404" 'applied "{\"op\":\"errpage-on\",\"code\":404}" && edge "$S" /gone.png "404 " && grep -q "<b>404</b>" <<<"$(body /gone.png)"'
ok "the page is a file of the website, editable" '[ -f "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/wp/.nostek-errors/404.html" ] && printf "<h1>Hilang</h1>\n" > "$T/404.html" && raw PUT "/v1/sites/$SID/files/content?path=/.nostek-errors/404.html&overwrite=1" "$T/404.html" && [ "$CODE" = 200 ] && grep -q "Hilang" <<<"$(body /gone.png)"'
ok "403 page on: a dot file gets it" 'applied "{\"op\":\"errpage-on\",\"code\":403}" && edge "$S" /.env "403 " && grep -q "<b>403</b>" <<<"$(body /.env)"'
ok "turning 404 on again changes nothing and keeps the edited page" 'change "{\"op\":\"errpage-on\",\"code\":404}"; [ "$CODE" = 200 ] && [ -z "$J" ] && grep -q Hilang "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/wp/.nostek-errors/404.html"'
printf 'x\n' > "$T/a.foo"; raw PUT "/v1/sites/$SID/files/content?path=/a.foo" "$T/a.foo"; printf 'y\n' > "$T/b.txt"; raw PUT "/v1/sites/$SID/files/content?path=/b.txt" "$T/b.txt"
ok "MIME types: a new extension and a replaced one" 'applied "{\"op\":\"mime-add\",\"type\":\"text/x-foo\",\"exts\":\"foo\"}" && applied "{\"op\":\"mime-add\",\"type\":\"text/x-custom\",\"exts\":\"txt\"}" && [ "$(hdr /a.foo)" = text/x-foo ] && [ "$(hdr /b.txt)" = text/x-custom ] && [ "$(hdr /logo.png)" = image/png ]'
ok "a content type that is not one is refused" 'change "{\"op\":\"mime-add\",\"type\":\"text/x;y\",\"exts\":\"y\"}"; [ "$CODE" = 400 ]'
ok "rules removed: standard pages and types again" 'applied "{\"op\":\"errpage-off\",\"code\":404}" && applied "{\"op\":\"errpage-off\",\"code\":403}" && applied "{\"op\":\"mime-remove\",\"type\":\"text/x-foo\"}" && applied "{\"op\":\"mime-remove\",\"type\":\"text/x-custom\"}" && ! grep -q "<b>403</b>" <<<"$(body /.env)" && [ "$(hdr /b.txt)" = text/plain ] && ! grep -q nostek-rules "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/nginx.conf"'

# --- the same on Apache (plain PHP website)
P="p.example.test"; PID="p-example-test"
printf '%s %s %s\n' "$IP" "$P" "$MARK" >> /etc/hosts
api POST /v1/sites "{\"app\":\"php\",\"domain\":\"$P\",\"aliases\":[],\"primary\":\"domain\",\"server\":\"apache\",\"php\":\"8.4\",\"admin_email\":\"\",\"title\":\"Situs P\"}"; J="$(jobid)"
ok "PHP website on Apache made" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && edge "$P" / "200 "'
api POST "/v1/sites/$PID/security" '{"op":"errpage-on","code":404}'; J="$(jobid)"; C1="$CODE"; S1="$(wait_job "$J")"
api POST "/v1/sites/$PID/security" '{"op":"mime-add","type":"text/x-foo","exts":"foo"}'; J="$(jobid)"; C2="$CODE"; S2="$(wait_job "$J")"
raw PUT "/v1/sites/$PID/files/content?path=/a.foo" "$T/a.foo"
pget() { curl -sk --noproxy '*' --max-time 20 --resolve "$P:443:127.0.0.1" "$@"; }
ok "Apache: the 404 page and the MIME type work" '[ "$C1" = 202 ] && [ "$S1" = done ] && [ "$C2" = 202 ] && [ "$S2" = done ] && edge "$P" /gone.png "404 " && grep -q "<b>404</b>" <<<"$(pget "https://$P/gone.png")" && pget -o /dev/null -D - "https://$P/a.foo" | tr -d "\r" | grep -qi "^content-type: text/x-foo"'
api POST "/v1/sites/$PID/security" '{"op":"errpage-off","code":404}'; J="$(jobid)"; wait_job "$J" >/dev/null
api POST "/v1/sites/$PID/security" '{"op":"mime-remove","type":"text/x-foo"}'; J="$(jobid)"; S3="$(wait_job "$J")"
ok "Apache: rules removed, the mount goes with them" '[ "$S3" = done ] && ! grep -q apache.conf "$SITE01_TEST_ROOT/srv/nostek/sites/$PID/compose.yml" && edge "$P" / "200 "'

# --- SEC-01 wave 3: login attempts from the edge's access log, malware scan with ClamAV
CONF="$SITE01_TEST_ROOT/etc/nostek-agent/config.json"
agent_restart() {
  stop_agents
  NOSTEK_AGENT_CONFIG="$CONF" DOCKER_CONFIG="$SITE01_TEST_ROOT/var/lib/nostek-agent/docker" nohup "$SITE01_TEST_ROOT/usr/local/sbin/nostek-agent" serve >> "$SITE01_TEST_ROOT/var/log/nostek-agent/test-agent.log" 2>&1 &
  for _ in $(seq 1 50); do api GET /v1/health; [ "$CODE" = 200 ] && return 0; sleep 0.2; done
  return 1
}
for _ in 1 2 3 4 5 6; do curl -sk --noproxy '*' --max-time 10 --resolve "$S:443:127.0.0.1" -o /dev/null -X POST --data 'log=admin&pwd=x' "https://$S/wp-login.php"; done
curl -sk --noproxy '*' --max-time 10 --resolve "$S:443:127.0.0.1" -o /dev/null "https://$S/wp-login.php"   # a GET is no login try
ok "the edge writes its access log, a few fields and no headers" '[ -s "$SITE01_TEST_ROOT/srv/nostek/edge/logs/access.log" ] && grep -q "\"RequestPath\":\"/wp-login.php\"" "$SITE01_TEST_ROOT/srv/nostek/edge/logs/access.log" && ! grep -qi "user-agent\|request_" "$SITE01_TEST_ROOT/srv/nostek/edge/logs/access.log"'
logins6() { api GET "/v1/sites/$SID/security"; python3 -c 'import json,sys; l=json.load(sys.stdin)["logins"]; sys.exit(0 if l["active"] and l["day"]==6 and l["ips"][0]["ip"]==sys.argv[1] and l["ips"][0]["last_path"]=="/wp-login.php" else 1)' "$GW" <<<"$BODY"; }
ok "login attempts counted per address within a minute (POSTs only)" 'for _ in $(seq 1 30); do logins6 && break; sleep 3; done; logins6'
ok "the address blocked from the list: marked blocked, then unblocked" 'applied "{\"op\":\"block-add\",\"ip\":\"$GW\"}" && api GET "/v1/sites/$SID/security" && grep -q "\"blocked\":true" <<<"$BODY" && applied "{\"op\":\"block-remove\",\"ip\":\"$GW\"}" && edge "$S" / "200 "'
ok "kept in logins.json (0600)" '[ "$(stat -c %a "$SITE01_TEST_ROOT/srv/nostek/security/logins.json")" = 600 ]'

printf 'harness-malware-sample-%s\n' "$COMMIT" > "$T/evil.php"
api POST "/v1/sites/$SID/files/mkdir" '{"path":"/wp-content"}'
raw PUT "/v1/sites/$SID/files/content?path=/wp-content/evil.php" "$T/evil.php"; C1="$CODE"
mkdir -p "$T/clamdb"; printf '%s:%s:Harness.Test.Sample\n' "$(md5sum < "$T/evil.php" | cut -d" " -f1)" "$(stat -c %s "$T/evil.php")" > "$T/clamdb/harness.hdb"; chmod -R a+rX "$T/clamdb"
python3 - "$CONF" "$T/clamdb" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); c["clam_test_db"] = sys.argv[2]
json.dump(c, open(sys.argv[1], "w"), indent=2)
PY
ok "test agent restarted with the harness signatures" '[ "$C1" = 200 ] && agent_restart'
api POST "/v1/sites/$SID/security/scan"; J="$(jobid)"; C2="$CODE"
ok "malware scan finds the planted file, and only it" '[ "$C2" = 202 ] && [ "$(wait_job "$J")" = done ] && api GET "/v1/sites/$SID/security" && python3 -c "import json,sys; s=json.load(sys.stdin)[\"scan\"][\"last\"]; f=s[\"found\"]; sys.exit(0 if len(f)==1 and f[0][\"path\"]==\"/wp-content/evil.php\" and f[0][\"signature\"].startswith(\"Harness.Test.Sample\") and s[\"files\"]>=3 and s[\"finished\"]>=s[\"started\"]>0 and not s.get(\"error\") else 1)" <<<"$BODY"'
ok "the scan ran without network and left no container" '! docker ps -aq --filter label=nostek.scan | grep -q . && grep -q "\"action\":\"malware-scan\"" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log"'
api POST "/v1/sites/$SID/files/delete" '{"path":"/wp-content/evil.php"}'
ok "moved to the file trash: shown as gone" 'api GET "/v1/sites/$SID/security" && grep -q "\"gone\":true" <<<"$BODY" && ! grep -q harness-malware-sample <<<"$(body /wp-content/evil.php)"'

# --- SEC-01 wave 3b: the web application firewall, the real nostek-waf binary in a container named like the core one
mkdir -p "$SITE01_TEST_ROOT/srv/nostek/edge/waf"; chmod 755 "$SITE01_TEST_ROOT/srv/nostek/edge/waf"
change '{"op":"waf-set","mode":"detect","level":1}'
ok "WAF refused while its service is missing" '[ "$CODE" = 400 ] && grep -q "PANEL-01 UPDATE" <<<"$BODY"'
WAFBIN="$T/nostek-waf"
(cd "$REPO/panel" && CGO_ENABLED=0 "$GO" build -trimpath -o "$WAFBIN" ./cmd/nostek-waf) >/dev/null 2>&1; chmod 755 "$WAFBIN"
docker run -d --name nostek-core-waf-1 --user 65532:65532 --read-only --cap-drop ALL --tmpfs /tmp --network nostek-edge \
  --entrypoint /nostek-waf -e WAF_CONFIG=/config -e WAF_LOG=/logs/waf.log \
  -v "$WAFBIN:/nostek-waf:ro" -v "$SITE01_TEST_ROOT/srv/nostek/edge/waf:/config:ro" -v "$SITE01_TEST_ROOT/srv/nostek/edge/logs:/logs" \
  --health-cmd "/nostek-waf healthcheck" --health-interval 2s --health-start-period 2s "$EDGEIMG" >/dev/null 2>&1
wafup() { for _ in $(seq 1 30); do [ "$(docker inspect -f '{{.State.Status}} {{.State.Health.Status}}' nostek-core-waf-1 2>/dev/null)" = "running healthy" ] && return 0; sleep 1; done; return 1; }
ok "WAF service built and healthy" '[ -x "$WAFBIN" ] && wafup'
SQLI="/?id=1%27%20OR%20%271%27=%271%27%20UNION%20SELECT%20password%20FROM%20users--"
HDR=(-H "User-Agent: Mozilla/5.0 (harness)" -H "Accept: text/html")
ok "WAF on in detect mode: an attack passes and is logged" 'applied "{\"op\":\"waf-set\",\"mode\":\"detect\",\"level\":1}" && grep -q "service: site-$SID-waf@file" "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/compose.yml" && edge "$S" "$SQLI" "200 " "${HDR[@]}" && edge "$S" / "200 " "${HDR[@]}" && grep -q "\"site\":\"$SID\".*\"action\":\"detected\"" "$SITE01_TEST_ROOT/srv/nostek/edge/logs/waf.log"'
ok "rules and edge files: rules 0644 for the WAF, no secret" '[ "$(stat -c %a "$SITE01_TEST_ROOT/srv/nostek/edge/waf/$SID.json")" = 644 ] && grep -q "np-site-$SID-web-1:80" "$SITE01_TEST_ROOT/srv/nostek/edge/waf/$SID.json" && grep -q "nostek-core-waf-1:8090" "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic/waf-$SID.yml"'
ok "block mode: the attack gets 403, a normal visit and a POST still reach the website (a static one answers 405)" 'applied "{\"op\":\"waf-set\",\"mode\":\"block\",\"level\":1}" && edge "$S" "$SQLI" "403 " "${HDR[@]}" && edge "$S" "/a?q=sepatu+murah" "200 " "${HDR[@]}" && edge "$S" /form "405 " "${HDR[@]}" -X POST --data "nama=Budi&pesan=halo"'
ok "an attack in a form body is blocked" 'edge "$S" /form "403 " "${HDR[@]}" -X POST --data "q=<script>alert(document.cookie)</script>"'
ok "a skipped path is not checked" 'applied "{\"op\":\"waf-skip-add\",\"path\":\"/free/\"}" && edge "$S" "/free/$SQLI" "200 " "${HDR[@]}" && edge "$S" "$SQLI" "403 " "${HDR[@]}"'
wafseen() { api GET "/v1/sites/$SID/security"; python3 -c 'import json,sys; w=json.load(sys.stdin)["waf"]; sys.exit(0 if w["on"] and w["mode"]=="block" and w["blocked"]>=1 and w["rules"] and all(r["rule"]!=949110 for r in w["rules"]) else 1)' <<<"$BODY"; }
ok "the agent shows the events and the rules that matched (within a minute)" 'for _ in $(seq 1 30); do wafseen && break; sleep 3; done; wafseen'
RULES="$(python3 -c 'import json,sys; print(" ".join(sorted({str(e["rule"]) for e in json.load(sys.stdin)["waf"]["events"] if "UNION" in e["uri"] and "/free/" not in e["uri"] and 911000<=e["rule"]<959000 and e["rule"]//1000!=949})))' <<<"$BODY")"
excl() { local r; for r in $RULES; do applied "{\"op\":\"waf-exclude-add\",\"rule\":$r}" || return 1; done; }
ok "switching off the rules that matched lets the visit through" '[ -n "$RULES" ] && excl && edge "$S" "$SQLI" "200 " "${HDR[@]}"'
ok "WAF off: an attack reaches the website again, the files go" 'applied "{\"op\":\"waf-off\"}" && edge "$S" /form "405 " "${HDR[@]}" -X POST --data "q=<script>alert(document.cookie)</script>" && [ ! -e "$SITE01_TEST_ROOT/srv/nostek/edge/waf/$SID.json" ] && [ ! -e "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic/waf-$SID.yml" ] && ! grep -q -- "-waf@file" "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/compose.yml"'
ok "audit records the WAF changes" 'grep -q "waf-set" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log" && grep -q "waf-off" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log"'

ok "no security router left in the website's labels" '! grep -E "site-$SID-(block|protect|hotlink|deny)" "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/compose.yml"'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$(docker ps -aq --filter name=np-site- --filter name=np-sec-edge --filter name=nostek-core-waf-1 | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT SEC-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT SEC-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
