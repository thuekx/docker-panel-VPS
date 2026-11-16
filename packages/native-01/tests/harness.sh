#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# NATIVE-01 harness: runs the package for real on a throwaway Ubuntu 24.04 VM (the CI runner): installs the PPA,
# Nginx, PHP-FPM 8.1-8.5, MariaDB and WP-CLI, then checks that only the edge network reaches Nginx, that MariaDB has
# no TCP port, that APPLY again changes nothing and that REVERT refuses while a native website exists.
# Never run it on a real server: it removes the runner's preinstalled Apache and MySQL first.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "${1:-$HERE/../native-01-host.sh}")"
[ "${NATIVE01_HARNESS_ON_THROWAWAY_VM:-0}" = 1 ] || { echo "REFUSE set NATIVE01_HARNESS_ON_THROWAWAY_VM=1 (CI runner only)"; exit 3; }
TESTS=0; BAD=0
CURL_IMG="curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69"
HOST="$(hostname)"; IP="$(ip -o -4 route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')"
A=(--ack NATIVE-01 --expect-ip "$IP" --expect-host "$HOST" --auto)
OUT=""; RC=0
run() { OUT="$(bash "$SCRIPT" "$@" 2>&1)"; RC=$?; }
has() { grep -qF -- "$1" <<<"$OUT"; }
ok() { TESTS=$((TESTS+1)); if eval "$2"; then echo "PASS t$TESTS $1"; else BAD=$((BAD+1)); echo "FAIL t$TESTS $1"; sed 's/^/    | /' <<<"$OUT" | tail -n 40; fi; }

# the runner image ships Apache and MySQL (stopped); a real VPS from HOST-02 has neither
export DEBIAN_FRONTEND=noninteractive
systemctl disable -q --now apache2 mysql >/dev/null 2>&1
apt-get purge -y -qq 'apache2*' 'mysql-server*' 'mysql-client*' 'mysql-common' >/dev/null 2>&1
rm -rf /etc/mysql /var/lib/mysql
mkdir -p /srv/nostek/sites /var/lib/nostek/site-01
docker network rm nostek-edge >/dev/null 2>&1; docker network create nostek-edge >/dev/null
GW="$(docker network inspect nostek-edge -f '{{(index .IPAM.Config 0).Gateway}}')"

run PREFLIGHT "${A[@]}"; ok "preflight fails before SITE-01" '[ $RC = 1 ] && has "SITE-01 is not applied"'
echo "phase=APPLIED" > /var/lib/nostek/site-01/state
run PREFLIGHT "${A[@]}"; ok "preflight passes (Ubuntu 24.04, edge network, port free, no TCP database)" '[ $RC = 0 ] && has "edge network" && has "host address $GW"'
run APPLY "${A[@]}" --expect-host other-host; ok "a wrong host name is refused" '[ $RC = 4 ] && has "WRONG HOST"'
OUT="$(env -u SUDO_USER bash "$SCRIPT" APPLY --ack NATIVE-01 --expect-ip "$IP" --expect-host "$HOST" 2>&1)"; RC=$?; ok "a root shell without sudo is refused" '[ $RC = 3 ] && has "no root shell"'

run APPLY "${A[@]}"; ok "apply installs the native engine" '[ $RC = 0 ] && has "RESULT NATIVE-01 APPLY PASS php=8.1,8.2,8.3,8.4,8.5 gateway=$GW"'
ok "php-fpm 8.1-8.5 present, the distribution pools off" '( for v in 8.1 8.2 8.3 8.4 8.5; do [ -x /usr/sbin/php-fpm$v ] || exit 1; ! systemctl is-active -q php$v-fpm || exit 1; done )'
ok "every PHP has the WordPress extensions" '( for v in 8.1 8.2 8.3 8.4 8.5; do m="$(php$v -m)"; for e in mysqli curl gd intl mbstring xml zip; do grep -qix "$e" <<<"$m" || { echo "php$v lacks $e"; exit 1; }; done; done )'
ok "WP-CLI runs" '/usr/local/bin/wp --allow-root --version | grep -q "WP-CLI 2.12.0"'
ok "nginx listens only on the edge address" '[ "$(ss -Htlnp | grep nginx | awk "{print \$4}" | sort -u | tr "\n" " ")" = "$GW:8088 " ]'
ok "a container on the edge network reaches nginx (unknown host closed with 444)" 'docker run --rm --network nostek-edge "$CURL_IMG" -s -o /dev/null --max-time 5 "http://$GW:8088/"; [ $? = 52 ]'
ok "the public address does not reach nginx" '! curl -s -o /dev/null --max-time 3 "http://$IP:8088/"'
ok "MariaDB answers on its socket and has no TCP port" 'mariadb -N -e "select 1" | grep -qx 1 && ! ss -Htln | awk "{print \$4}" | grep -qE ":3306$"'
ok "the agent is told (native.json)" 'grep -q "\"gateway\": \"$GW\"" /etc/nostek-agent/native.json && python3 -m json.tool /etc/nostek-agent/native.json >/dev/null'
ok "the Traefik file-provider folder exists" '[ -d /srv/nostek/edge/dynamic ]'
run VERIFY "${A[@]}"; ok "verify passes" '[ $RC = 0 ] && has "RESULT NATIVE-01 VERIFY PASS native_sites=0 sisa_uji=0"'
run APPLY "${A[@]}"; ok "apply again passes and changes nothing" '[ $RC = 0 ] && has "port 8088 used by our Nginx" && has "WP-CLI 2.12.0 (sha512 checked)"'

# a PHP pool as the agent will write it starts as an unprivileged user
useradd --system --no-create-home --shell /usr/sbin/nologin np-harness >/dev/null 2>&1
mkdir -p /run/np-harness && chown np-harness /run/np-harness
cat > /tmp/np-harness-fpm.conf <<'EOF'
[global]
error_log = /proc/self/fd/2
[h]
listen = /run/np-harness/php.sock
pm = ondemand
pm.max_children = 2
EOF
chmod 0644 /tmp/np-harness-fpm.conf
OUT="$(runuser -u np-harness -- php-fpm8.4 -t -y /tmp/np-harness-fpm.conf 2>&1)"; RC=$?
ok "php-fpm 8.4 accepts a website pool as the website's own user (no root)" '[ $RC = 0 ]'

mkdir -p /srv/nostek/sites/n-harness && echo '{"id":"n-harness","engine": "native"}' > /srv/nostek/sites/n-harness/site.json
run REVERT "${A[@]}"; ok "revert refuses while a native website exists" '[ $RC = 3 ] && has "1 native website(s) still exist" && systemctl is-active -q nginx'
rm -rf /srv/nostek/sites/n-harness
run REVERT "${A[@]}"; ok "revert stops the native engine and keeps the packages" '[ $RC = 0 ] && ! systemctl is-active -q nginx && [ ! -e /etc/nostek-agent/native.json ] && [ -x /usr/sbin/php-fpm8.4 ]'
run VERIFY "${A[@]}"; ok "verify fails after revert" '[ $RC = 1 ]'
run APPLY "${A[@]}"; ok "apply after revert brings it back" '[ $RC = 0 ] && systemctl is-active -q nginx'

docker network rm nostek-edge >/dev/null 2>&1
if [ "$BAD" = 0 ]; then echo "RESULT NATIVE-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT NATIVE-01 HARNESS FAIL bad=$BAD/$TESTS"; exit 1
