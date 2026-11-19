#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# DOM-01 harness (real Docker and a real Traefik edge): a WordPress website is made while its domain still points
# elsewhere (only the preview name is served), gets redirects and an extra name, then DNS is switched and "check
# DNS again" makes the domain its main address and moves WordPress's addresses. Names resolve through /etc/hosts
# lines that this harness adds, changes and removes (marker "np-dom01-harness"). The edge has a certificate
# resolver that cannot reach any CA, so every name shows the edge's default certificate ("being issued").
# Needs: root, Docker with compose, git, curl, python3.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "$HERE/../site-01-agent.sh")"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
STUBS="$REPO/packages/panel-01/tests/stubs"
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"
IP=198.51.100.7; ELSE=192.0.2.50; ZONE=preview.example.test; D=panel.example.test
MARK="# np-dom01-harness"
EDGEIMG="$(grep -o 'traefik:v[^ ]*@sha256:[0-9a-f]*' "$REPO/deploy/core/compose.yml" | head -n1)"
PRE="d-example-test.$ZONE"

stop_agents() { pkill -f "nostek-agent serve" >/dev/null 2>&1; sleep 0.5; }
cleanup() {
  stop_agents
  docker ps -aq --filter label=com.docker.compose.project | while read -r id; do
    p="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id" 2>/dev/null)"
    case "$p" in np-site-*) docker rm -f "$id" >/dev/null 2>&1 ;; esac
  done
  docker rm -f np-dom-edge >/dev/null 2>&1
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
jobid() { grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4; }
wait_job() { # job [tries of 3 s] -> state; the job body stays in $T/job.json
  local s=""
  for _ in $(seq 1 "${2:-200}"); do
    api GET "/v1/jobs/$1"; s="$(grep -o '"state":"[a-z]*"' <<<"$BODY" | cut -d'"' -f4)"
    [ "$s" != running ] && break; sleep 3
  done
  printf '%s' "$BODY" > "$T/job.json"; echo "$s"
}
JOB() { cat "$T/job.json"; }
change() { api POST /v1/sites/d-example-test/domains "$1"; J="$(jobid)"; }
# visit NAME PATH -> "code redirect" through the edge (the edge's default certificate is not trusted: -k)
visit() { curl -sk --noproxy '*' --max-time 20 --resolve "$1:443:127.0.0.1" -o /dev/null -w '%{http_code} %{redirect_url}' "https://$1$2"; }
page() { curl -sk --noproxy '*' --max-time 20 --resolve "$1:443:127.0.0.1" "https://$1$2"; }
# edge NAME PATH WANT: the edge may need a few seconds to see a recreated container
edge() { local got=""; for _ in $(seq 1 20); do got="$(visit "$1" "$2")"; [ "$got" = "$3" ] && return 0; sleep 1; done; echo "    | $1$2 -> $got (want $3)"; return 1; }
hosts() { # NAME IP|"" : point a name, or remove it
  sed -i "/ $1 $MARK\$/d" /etc/hosts
  [ -n "$2" ] && printf '%s %s %s\n' "$2" "$1" "$MARK" >> /etc/hosts
  return 0
}
cli() { api POST "/v1/sites/d-example-test/wp/cli" "$(python3 -c 'import json,sys; print(json.dumps({"line": sys.argv[1]}))' "$1")"; python3 -c 'import json,sys; print(json.load(sys.stdin).get("output","").strip())' <<<"$BODY" 2>/dev/null; }
host_field() { python3 -c 'import json,sys; h=[x for x in json.load(sys.stdin)["hosts"] if x["name"]==sys.argv[1]][0]; v=h.get(sys.argv[2], ""); print(v if isinstance(v, str) else json.dumps(v))' "$1" "$2" <<<"$BODY" 2>/dev/null; }

[ -n "$EDGEIMG" ] || { echo "RESULT DOM-01 HARNESS FAIL edge image not found"; exit 1; }
cleanup; TMP_ALL="$(mktemp -d)"; T="$(mktemp -d "$TMP_ALL/t.XXXX")"
export SITE01_TEST_ROOT="$T/root" SITE01_TEST_REPO="file://$REPO"
mkdir -p "$SITE01_TEST_ROOT/var/lib/nostek/panel-01"; echo "phase=APPLIED" > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"
docker network create nostek-edge >/dev/null
docker run -d --name np-dom-edge --network nostek-edge -p 127.0.0.1:443:8443 -v /var/run/docker.sock:/var/run/docker.sock:ro "$EDGEIMG" \
  --global.checkNewVersion=false --global.sendAnonymousUsage=false --log.level=INFO \
  --entryPoints.websecure.address=:8443 --entryPoints.websecure.http.tls=true \
  --providers.docker=true --providers.docker.exposedByDefault=false --providers.docker.network=nostek-edge \
  --certificatesResolvers.le.acme.storage=/tmp/acme.json --certificatesResolvers.le.acme.caServer=https://127.0.0.1:9/directory \
  --certificatesResolvers.le.acme.tlsChallenge=true >/dev/null
hosts "$PRE" "$IP"; hosts d.example.test "$ELSE"; hosts www.d.example.test "$ELSE"
export PATH="$STUBS:/usr/sbin:/usr/bin:/sbin:/bin" SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=$IP FAKE_DNS=$IP
OUT="$(bash "$SCRIPT" APPLY --ack SITE-01 --expect-ip "$IP" --expect-host fake-vps --domain "$D" --preview-zone "$ZONE" --src-commit "$COMMIT" 2>&1 </dev/null)"; RC=$?
ok "agent installed" '[ $RC = 0 ] && grep -q "RESULT SITE-01 APPLY PASS" <<<"$OUT"'
OUT=""

# --- a WordPress website whose domain still points to the old host
api POST /v1/sites '{"domain":"d.example.test","aliases":[],"primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"Situs D"}'; J="$(jobid)"
ok "website made on its preview name only" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && edge "$PRE" / "200 " && grep -q "<title>Situs D" <<<"$(page "$PRE" /)"'
SD="$SITE01_TEST_ROOT/srv/nostek/sites/d-example-test"
cli "post create --post_title=Tautan --post_status=publish --post_name=tautan --post_content=https://$PRE/kontak/" >/dev/null
ok "the domain is not served yet" '! grep -q "Host(\`d.example.test\`)" "$SD/compose.yml" && [ "$(visit d.example.test /)" = "404 " ]'

api GET /v1/sites/d-example-test/domains
ok "DNS check: domain elsewhere, preview here and served, certificate being issued" '[ "$CODE" = 200 ] && [ "$(host_field d.example.test dns)" = elsewhere ] && [ "$(host_field d.example.test ipv4)" = "[\"$ELSE\"]" ] && [ "$(host_field "$PRE" dns)" = here ] && [ "$(host_field "$PRE" routed)" = true ] && [ "$(host_field "$PRE" cert)" = pending ]'
change '{"op":"refresh"}'
ok "check DNS again with nothing new: no job, nothing changes" '[ "$CODE" = 200 ] && [ -z "$J" ]'

# --- redirects, through the real edge
change '{"op":"redirect-add","from":"/old-page","to":"/new-page","code":301}'
ok "redirect 301 added" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ]'
change '{"op":"redirect-add","from":"/promo","to":"https://shop.example.test/x?ref=1","code":302}'
ok "redirect 302 added" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ]'
ok "/old-page goes 301 to /new-page with its query" 'edge "$PRE" "/old-page?a=1" "301 https://$PRE/new-page?a=1"'
ok "/old-page/ (closing slash) too" 'edge "$PRE" /old-page/ "301 https://$PRE/new-page"'
ok "/promo goes 302 to the other address, its own query kept" 'edge "$PRE" "/promo?x=9" "302 https://shop.example.test/x?ref=1"'
ok "a longer path is not redirected" '[ "$(visit "$PRE" /old-page-two | cut -d" " -f1)" != 301 ]'
change '{"op":"redirect-add","from":"/x","to":"/y$1","code":301}'
ok "a target with \$ is refused" '[ "$CODE" = 400 ]'
change '{"op":"redirect-add","from":"/.well-known/acme-challenge/a","to":"/y","code":301}'
ok "/.well-known cannot be redirected" '[ "$CODE" = 400 ]'

# --- an extra name
hosts a.example.test "$IP"
change '{"op":"add","name":"a.example.test"}'
ok "extra name pointing here is added and served" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && edge a.example.test /halo "301 https://$PRE/halo"'
change '{"op":"add","name":"b.example.test"}'
ok "extra name without DNS is listed, not served" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && ! grep -q "b.example.test" "$SD/compose.yml" && grep -q "\"b.example.test\"" "$SD/site.json"'
for bad in "$D" "x.$ZONE" "a.example.test" "Bad_Name"; do
  change "{\"op\":\"add\",\"name\":\"$bad\"}"; ok "name $bad refused" '[ "$CODE" = 400 ]'
done
change "{\"op\":\"remove\",\"name\":\"$PRE\"}"
ok "the preview name cannot be removed" '[ "$CODE" = 400 ]'

# --- DNS switched to this server: one check moves the website to its domain
hosts d.example.test "$IP"; hosts www.d.example.test "$IP"
sleep 6 # Go reads /etc/hosts again after 5 s
change '{"op":"refresh"}'
ok "check DNS again: routes, addresses, check" '[ "$CODE" = 202 ] && [ "$(wait_job "$J" 100)" = done ] && grep -q "\"steps\":\[\"routes\",\"addresses\",\"check\"\]" <<<"$(JOB)"'
ok "the domain serves the website" 'edge d.example.test / "200 " && grep -q "<title>Situs D" <<<"$(page d.example.test /)"'
ok "www, the preview name and the extra name go 301 to the domain" 'edge www.d.example.test /p "301 https://d.example.test/p" && edge "$PRE" /p "301 https://d.example.test/p" && edge a.example.test / "301 https://d.example.test/"'
ok "WordPress home and siteurl are the domain" '[ "$(cli "option get home")" = https://d.example.test ] && [ "$(cli "option get siteurl")" = https://d.example.test ]'
ok "links in posts moved to the domain" 'P="$(page d.example.test /tautan/)"; grep -q "https://d.example.test/kontak/" <<<"$P" && ! grep -q "$PRE" <<<"$P"'
ok "redirects follow the new main address" 'edge d.example.test /old-page "301 https://d.example.test/new-page"'
api GET /v1/sites/d-example-test
ok "site.json: address and main name are the domain" 'grep -q "\"url\":\"https://d.example.test\"" <<<"$BODY"'
api GET /v1/sites/d-example-test/domains
ok "DNS check: domain here and served" '[ "$(host_field d.example.test dns)" = here ] && [ "$(host_field d.example.test routed)" = true ] && grep -q "\"canonical\":\"d.example.test\"" <<<"$BODY"'

# --- removing
change '{"op":"remove","name":"a.example.test"}'
ok "extra name removed and no longer served" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && edge a.example.test / "404 "'
change '{"op":"redirect-remove","from":"/old-page"}'
ok "redirect removed" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && [ "$(visit d.example.test /old-page | cut -d" " -f1)" != 301 ] && edge d.example.test /promo "302 https://shop.example.test/x?ref=1"'
change '{"op":"remove","name":"d.example.test"}'
ok "the main domain cannot be removed" '[ "$CODE" = 400 ]'
ok "the audit log names the changes" 'grep -q "\"action\":\"domains\",\"site\":\"d-example-test\",\"result\":\"ok\",\"detail\":\"add a.example.test job" "$SITE01_TEST_ROOT/var/log/nostek-agent/audit.log"'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$(docker ps -aq --filter name=np-site- --filter name=np-dom-edge | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT DOM-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT DOM-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
