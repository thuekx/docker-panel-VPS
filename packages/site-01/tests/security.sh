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
  docker rm -f np-sec-edge >/dev/null 2>&1
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
mkdir -p "$SITE01_TEST_ROOT/var/lib/nostek/panel-01" "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic"; echo "phase=APPLIED" > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"
chmod 0755 "$SITE01_TEST_ROOT" "$SITE01_TEST_ROOT/srv" "$SITE01_TEST_ROOT/srv/nostek" "$SITE01_TEST_ROOT/srv/nostek/edge" "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic"
docker network create nostek-edge >/dev/null
GW="$(docker network inspect nostek-edge -f '{{(index .IPAM.Config 0).Gateway}}')"
docker run -d --name np-sec-edge --user 65532:65532 --group-add "$(stat -c %g /var/run/docker.sock)" --network nostek-edge -p 127.0.0.1:443:8443 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro -v "$SITE01_TEST_ROOT/srv/nostek/edge/dynamic:/dynamic:ro" "$EDGEIMG" \
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
ok "no security router left in the website's labels" '! grep -E "site-$SID-(block|protect|hotlink|deny)" "$SITE01_TEST_ROOT/srv/nostek/sites/$SID/compose.yml"'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$(docker ps -aq --filter name=np-site- --filter name=np-sec-edge | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT SEC-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT SEC-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
