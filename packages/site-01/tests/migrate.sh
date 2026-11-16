#!/usr/bin/env bash
# shellcheck disable=SC2034  # RC is read inside eval
# MIG-01 harness (real Docker): an "old site" WordPress with its own MariaDB, table prefix np_, binary/NULL/emoji
# rows and a table without a primary key, served by a throwaway HTTPS proxy with the Nostek Migrate plugin, is
# moved into a new website of the agent through the plugin. Then cPanel backups are moved: a full backup (nested
# homedir.tar, two WordPress folders: the job asks which one) by upload, and a home backup with a separate .sql.gz
# by download link plus upload. Needs: root, Docker with compose, git, curl, python3, openssl.
# Names resolve through /etc/hosts lines that this harness adds and removes (marker "np-mig01-harness").
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$(realpath "$HERE/../site-01-agent.sh")"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
COMMIT="$(git -C "$REPO" rev-parse HEAD)"
STUBS="$REPO/packages/panel-01/tests/stubs"
PLUGIN="$REPO/panel/web/plugin/nostek-migrate.php"
CAT="$REPO/agent/internal/catalog/catalog.go"
TESTS=0; BAD=0; TMP_ALL="$(mktemp -d)"
IP=198.51.100.7; ZONE=preview.example.test; D=panel.example.test
MARK="# np-mig01-harness"
CURL_IMG="curlimages/curl:8.11.1@sha256:c1fe1679c34d9784c1b0d1e5f62ac0a79fca01fb6377cdd33e90473c6f9f9a69"
WPIMG="$(grep -o 'wordpress:7[^"]*-php8.4-apache@sha256:[0-9a-f]*' "$CAT" | head -n1)"
CLIIMG="$(grep -o 'wordpress:cli[^"]*php8.4@sha256:[0-9a-f]*' "$CAT" | head -n1)"
DBIMG="$(grep -o 'mariadb:11.8@sha256:[0-9a-f]*' "$CAT" | head -n1)"
NGXIMG="$(grep -o 'nginx:[^"]*@sha256:[0-9a-f]*' "$CAT" | head -n1)"
OLDENV=(-e WORDPRESS_DB_HOST=np-mig-old-db -e WORDPRESS_DB_NAME=oldwp -e WORDPRESS_DB_USER=old -e WORDPRESS_DB_PASSWORD=old-db-pass -e WORDPRESS_TABLE_PREFIX=np_
  -e "WORDPRESS_CONFIG_EXTRA=if (isset(\$_SERVER['HTTP_X_FORWARDED_PROTO']) && \$_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { \$_SERVER['HTTPS'] = 'on'; }")

stop_agents() { pkill -f "nostek-agent serve" >/dev/null 2>&1; sleep 0.5; }
cleanup() {
  stop_agents
  docker ps -aq --filter label=com.docker.compose.project | while read -r id; do
    p="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id" 2>/dev/null)"
    case "$p" in np-site-*) docker rm -f "$id" >/dev/null 2>&1 ;; esac
  done
  docker rm -f np-mig-old-wp np-mig-old-db np-mig-old-tls >/dev/null 2>&1
  docker network ls --format '{{.Name}}' | grep -E '^np-site-' | xargs -r docker network rm >/dev/null 2>&1
  docker network rm nostek-edge np-mig-old >/dev/null 2>&1
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
raw() { # METHOD PATH FILE
  local out
  out="$(curl -s --noproxy '*' --max-time 600 --unix-socket "$(SOCK)" -X "$1" -H 'Content-Type: application/octet-stream' --data-binary "@$3" -w '\n%{http_code}' "http://agent$2")"
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
jobid() { grep -o '"job":"[^"]*"' <<<"$BODY" | cut -d'"' -f4; }
wait_job() { # job [tries of 3 s] -> state; the job body stays in $JOB
  local s=""
  for _ in $(seq 1 "${2:-200}"); do
    api GET "/v1/jobs/$1"; s="$(grep -o '"state":"[a-z]*"' <<<"$BODY" | cut -d'"' -f4)"
    [ "$s" != running ] && break; sleep 3
  done
  JOB="$BODY"; echo "$s"
}
site_body() { docker run --rm --network nostek-edge "$CURL_IMG" -s --max-time 20 -H "Host: $2" -H 'X-Forwarded-Proto: https' "http://np-site-$1-web-1$3"; }
cli() { api POST "/v1/sites/$1/wp/cli" "$(python3 -c 'import json,sys; print(json.dumps({"line": sys.argv[1]}))' "$2")"; python3 -c 'import json,sys; print(json.load(sys.stdin).get("output","").strip())' <<<"$BODY" 2>/dev/null; }
sql() { api POST "/v1/sites/$1/db/query" "$(python3 -c 'import json,sys; print(json.dumps({"sql": sys.argv[1]}))' "$2")"; echo "$BODY"; }
wpo() { docker run --rm --user 33:33 --network np-mig-old -v "$T/old:/var/www/html" "${OLDENV[@]}" "$CLIIMG" wp "$@" 2>/dev/null; }
PLUG() { curl -s --noproxy '*' --max-time 30 --cacert "$T/tls/tls.crt" -o /dev/null -w '%{http_code}' "$@"; }
agent_restart() { # restart the test agent on its current config
  stop_agents
  NOSTEK_AGENT_CONFIG="$CONF" DOCKER_CONFIG="$SITE01_TEST_ROOT/var/lib/nostek-agent/docker" nohup "$SITE01_TEST_ROOT/usr/local/sbin/nostek-agent" serve >> "$SITE01_TEST_ROOT/var/log/nostek-agent/test-agent.log" 2>&1 &
  for _ in $(seq 1 50); do api GET /v1/health; [ "$CODE" = 200 ] && return 0; sleep 0.2; done
  return 1
}

[ -n "$WPIMG" ] && [ -n "$CLIIMG" ] && [ -n "$DBIMG" ] && [ -n "$NGXIMG" ] || { echo "RESULT MIG-01 HARNESS FAIL images not found in the catalogue"; exit 1; }
cleanup; TMP_ALL="$(mktemp -d)"; T="$(mktemp -d "$TMP_ALL/t.XXXX")"
export SITE01_TEST_ROOT="$T/root" SITE01_TEST_REPO="file://$REPO"
mkdir -p "$SITE01_TEST_ROOT/var/lib/nostek/panel-01"; echo "phase=APPLIED" > "$SITE01_TEST_ROOT/var/lib/nostek/panel-01/state"
docker network create nostek-edge >/dev/null
export PATH="$STUBS:/usr/sbin:/usr/bin:/sbin:/bin" SUDO_USER=testadmin FAKE_HOSTNAME=fake-vps FAKE_LOCAL_IP=$IP FAKE_DNS=$IP
CONF="$SITE01_TEST_ROOT/etc/nostek-agent/config.json"
OUT="$(bash "$SCRIPT" APPLY --ack SITE-01 --expect-ip "$IP" --expect-host fake-vps --domain "$D" --preview-zone "$ZONE" --src-commit "$COMMIT" 2>&1 </dev/null)"; RC=$?
ok "agent installed" '[ $RC = 0 ] && grep -q "RESULT SITE-01 APPLY PASS" <<<"$OUT"'
OUT=""

# --- the old site: MariaDB, WordPress (prefix np_), HTTPS proxy with a throwaway certificate
docker network create np-mig-old >/dev/null
mkdir -p "$T/old" "$T/tls"; chown 33:33 "$T/old"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=old.example.test -addext subjectAltName=DNS:old.example.test \
  -keyout "$T/tls/tls.key" -out "$T/tls/tls.crt" >/dev/null 2>&1; chmod 644 "$T/tls/tls.key"
cat > "$T/nginx.conf" <<'EOF'
server {
  listen 443 ssl; server_name old.example.test;
  ssl_certificate /t/tls.crt; ssl_certificate_key /t/tls.key;
  client_max_body_size 0; proxy_buffering off; proxy_read_timeout 120s;
  location / { proxy_pass http://np-mig-old-wp; proxy_set_header Host $host; proxy_set_header X-Forwarded-Proto https; }
}
EOF
docker run -d --name np-mig-old-db --network np-mig-old -e MARIADB_DATABASE=oldwp -e MARIADB_USER=old -e MARIADB_PASSWORD=old-db-pass -e MARIADB_RANDOM_ROOT_PASSWORD=1 "$DBIMG" >/dev/null
for _ in $(seq 1 60); do docker exec np-mig-old-db healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1 && break; sleep 2; done
docker run -d --name np-mig-old-wp --network np-mig-old -v "$T/old:/var/www/html" "${OLDENV[@]}" "$WPIMG" >/dev/null
for _ in $(seq 1 60); do [ -f "$T/old/wp-config.php" ] && [ -f "$T/old/wp-includes/version.php" ] && break; sleep 2; done
docker run -d --name np-mig-old-tls --network np-mig-old -v "$T/tls:/t:ro" -v "$T/nginx.conf:/etc/nginx/conf.d/default.conf:ro" "$NGXIMG" >/dev/null
TLS_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' np-mig-old-tls)"
OLDNET="$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' np-mig-old)"
printf '%s old.example.test %s\n%s m-example-test.%s %s\n' "$TLS_IP" "$MARK" "$IP" "$ZONE" "$MARK" >> /etc/hosts
wpo core install --url=https://old.example.test --title="Situs Lama" --admin_user=oldadmin --admin_password=Old-Admin-Pass-123 --admin_email=o@example.test --skip-email >/dev/null
wpo rewrite structure '/%postname%/' --hard >/dev/null
wpo post create --post_title="Halo dari situs lama" --post_status=publish --post_content='<img src="https://old.example.test/wp-content/uploads/2026/11/a.jpg"> <a href="http://old.example.test/kontak/">kontak</a>' >/dev/null
wpo option add np_serial '{"url":"https://old.example.test/x","n":3}' --format=json >/dev/null
wpo db query "CREATE TABLE np_bin (id INT PRIMARY KEY, b VARBINARY(16), t VARCHAR(32) CHARACTER SET utf8mb4, n INT NULL) DEFAULT CHARSET=utf8mb4; INSERT INTO np_bin VALUES (1, 0x00FF27225C0A, 'kopi ☕ 🚀', NULL), (2, NULL, 'O''Brien \\\\ x', 7); CREATE TABLE np_nopk (a INT, b TEXT); INSERT INTO np_nopk VALUES (1,'a'),(1,'a'),(2,NULL); CREATE TABLE other_app (id INT);" >/dev/null
mkdir -p "$T/old/wp-content/uploads/2026/11" "$T/old/wp-content/cache" "$T/old/wp-content/plugins/nostek-migrate"
printf 'JPEG' > "$T/old/wp-content/uploads/2026/11/a.jpg"
head -c 70000000 /dev/urandom > "$T/old/wp-content/uploads/2026/11/big.bin"   # above 64 MB: comes in ranges
printf 'cached' > "$T/old/wp-content/cache/page.html"; printf '<?php // old host cache' > "$T/old/wp-content/advanced-cache.php"
ln -s ../uploads "$T/old/wp-content/link-to-uploads"
cp "$PLUGIN" "$T/old/wp-content/plugins/nostek-migrate/nostek-migrate.php"
chown -R 33:33 "$T/old"
wpo plugin activate nostek-migrate >/dev/null
CODE_PL="$(wpo eval 'echo Nostek_Migrate::new_key();')"
KEY="$(python3 -c 'import base64,json,sys; s=sys.argv[1][4:]; print(json.loads(base64.urlsafe_b64decode(s+"="*(-len(s)%4)))["k"])' "$CODE_PL" 2>/dev/null)"
H="Host: old.example.test"
ok "old site answers over HTTPS with its title" 'curl -s --noproxy "*" --cacert "$T/tls/tls.crt" https://old.example.test/ | grep -q "<title>Situs Lama"'
ok "plugin code made for the https address" '[[ "$CODE_PL" == nm1.* ]] && [ ${#KEY} = 43 ]'

# --- the plugin on its own: keys, paths, the tar stream and the SQL pieces
ok "plugin: no key and a wrong key are refused" '[ "$(PLUG https://old.example.test/wp-json/nostek-migrate/v1/info)" = 403 ] && [ "$(PLUG -H "X-Nostek-Key: wrong" https://old.example.test/wp-json/nostek-migrate/v1/info)" = 403 ]'
ok "plugin: info with the key" '[ "$(PLUG -H "X-Nostek-Key: $KEY" https://old.example.test/wp-json/nostek-migrate/v1/info)" = 200 ]'
ok "plugin: paths leaving the site refused" '[ "$(PLUG -H "X-Nostek-Key: $KEY" -H "Content-Type: application/json" --data "{\"paths\":[\"../../etc/passwd\"]}" https://old.example.test/wp-json/nostek-migrate/v1/files)" = 400 ] && [ "$(PLUG -H "X-Nostek-Key: $KEY" "https://old.example.test/wp-json/nostek-migrate/v1/file?path=/etc/passwd&offset=0&length=10")" = 400 ]'
curl -s --noproxy '*' --cacert "$T/tls/tls.crt" -H "X-Nostek-Key: $KEY" -H 'Content-Type: application/json' --data '{"paths":["index.php","wp-content/link-to-uploads","wp-content/uploads/2026/11/a.jpg"]}' https://old.example.test/wp-json/nostek-migrate/v1/files -o "$T/batch.tar"
ok "plugin: tar stream readable, link kept as a link" 'python3 -c "import tarfile,sys; t=tarfile.open(sys.argv[1]); m={x.name:x for x in t}; assert m[\"wp-content/link-to-uploads\"].issym() and m[\"wp-content/link-to-uploads\"].linkname==\"../uploads\"; assert t.extractfile(m[\"wp-content/uploads/2026/11/a.jpg\"]).read()==b\"JPEG\"" "$T/batch.tar"'

# --- the new website
api POST /v1/sites '{"domain":"m.example.test","aliases":[],"primary":"domain","server":"apache","php":"8.4","admin_email":"o@example.test","title":"Situs M"}'; J="$(jobid)"
sleep 6
ok "new WordPress website made" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = done ] && grep -q "<title>Situs M" <<<"$(site_body m-example-test m-example-test.$ZONE /)"'
SM="$SITE01_TEST_ROOT/srv/nostek/sites/m-example-test"; WPM="$SM/wp"
MCFG="$(cat "$WPM/wp-config.php")"

# the guard: the old site is on a private Docker network, which a real server never reaches
api POST /v1/sites/m-example-test/migrate "{\"way\":\"plugin\",\"code\":\"$CODE_PL\"}"; J="$(jobid)"
ok "private address refused by the URL guard, nothing changed" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = failed ] && grep -q "public internet address" <<<"$JOB" && [ ! -e "$SM/backups" -o -z "$(ls "$SM/backups" 2>/dev/null)" ]'

python3 - "$CONF" "$OLDNET" "$T/tls/tls.crt" <<'EOF'
import json, sys
c = json.load(open(sys.argv[1])); c["migrate_test_allow"] = [sys.argv[2]]; c["migrate_test_ca"] = sys.argv[3]
json.dump(c, open(sys.argv[1], "w"), indent=2)
EOF
ok "test agent restarted with the harness allow-list" 'agent_restart'

api POST /v1/sites/m-example-test/migrate '{"way":"plugin","code":"nm1.eyJ1IjoiaHR0cHM6Ly9vbGQuZXhhbXBsZS50ZXN0LyIsImsiOiJ4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHgifQ"}'; J="$(jobid)"
ok "a wrong code is refused by the plugin" '[ "$CODE" = 202 ] && [ "$(wait_job "$J")" = failed ] && grep -q "403" <<<"$JOB"'

OLDHASH="$(wpo db query "SELECT user_pass FROM np_users WHERE user_login='oldadmin'" --skip-column-names)"
api POST /v1/sites/m-example-test/migrate "{\"way\":\"plugin\",\"code\":\"$CODE_PL\"}"; J="$(jobid)"
ST="$(wait_job "$J" 400)"
ok "migration by plugin done" '[ "$ST" = done ] && grep -q "done https://old.example.test → https://m-example-test.$ZONE" <<<"$JOB"'
grep -o '"steps":\[[^]]*\]' <<<"$JOB" | sed 's/^/    | /'
ok "the moved site answers with the old title" 'grep -q "<title>Situs Lama" <<<"$(site_body m-example-test m-example-test.$ZONE /)"'
ok "the post came along, its links point at the new address" 'P="$(site_body m-example-test m-example-test.$ZONE /halo-dari-situs-lama/)"; grep -q "Halo dari situs lama" <<<"$P" && grep -q "https://m-example-test.$ZONE/wp-content/uploads/2026/11/a.jpg" <<<"$P" && grep -q "https://m-example-test.$ZONE/kontak/" <<<"$P" && ! grep -q "old.example.test" <<<"$P"'
ok "home and siteurl are the new address" '[ "$(cli m-example-test "option get home")" = "https://m-example-test.$ZONE" ] && [ "$(cli m-example-test "option get siteurl")" = "https://m-example-test.$ZONE" ]'
ok "serialized option rewritten and still readable" '[ "$(cli m-example-test "option get np_serial --format=json")" = "{\"url\":\"https:\/\/m-example-test.$ZONE\/x\",\"n\":3}" ]'
ok "the old administrator can log in (same password hash)" '[ -n "$OLDHASH" ] && grep -qF "$OLDHASH" <<<"$(sql m-example-test "SELECT user_pass FROM np_users WHERE user_login = '"'"'oldadmin'"'"'")"'
OLDBIN="$(wpo db query "SELECT id, HEX(b), HEX(t), n IS NULL FROM np_bin ORDER BY id" --skip-column-names | tr '\t\n' ',;')"
NEWBIN="$(sql m-example-test "SELECT id, HEX(b), HEX(t), n IS NULL FROM np_bin ORDER BY id" | python3 -c 'import json,sys; r=json.load(sys.stdin)["results"][0]["rows"]; print(";".join(",".join("NULL" if v is None else str(v) for v in x) for x in r)+";")' 2>/dev/null)"
ok "binary, NULL and emoji rows identical" '[ -n "$OLDBIN" ] && [ "$OLDBIN" = "$NEWBIN" ]'
ok "table without a primary key copied whole, other apps' tables left out" 'grep -q "\"3\"" <<<"$(sql m-example-test "SELECT COUNT(*) FROM np_nopk")" && ! grep -q other_app <<<"$(sql m-example-test "SHOW TABLES")"'
ok "large file (ranges) identical, link kept, cache left behind, drop-in moved aside" '[ "$(sha256sum < "$WPM/wp-content/uploads/2026/11/big.bin")" = "$(sha256sum < "$T/old/wp-content/uploads/2026/11/big.bin")" ] && [ "$(readlink "$WPM/wp-content/link-to-uploads")" = ../uploads ] && [ ! -e "$WPM/wp-content/cache/page.html" ] && [ -e "$WPM/wp-content/advanced-cache.php.nostek-off" ]'
ok "files belong to www-data; the panel wp-config.php kept, the old one guarded" '[ "$(stat -c %u "$WPM/index.php")" = 33 ] && [ "$(cat "$WPM/wp-config.php")" = "$MCFG" ] && head -c 14 "$WPM/wp-config.old.php" | grep -q "<?php exit;" && [ "$(site_body m-example-test m-example-test.$ZONE /wp-config.old.php)" = "" ]'
ok "prefix np_ carried in compose and site.json" 'grep -q "WORDPRESS_TABLE_PREFIX: np_" "$SM/compose.yml" && grep -q "\"table_prefix\": *\"np_\"" "$SM/site.json"'
ok "the plugin and its key are gone on the new site" '[ ! -e "$WPM/wp-content/plugins/nostek-migrate" ] && [ -z "$(cli m-example-test "option get nostek_migrate_key")" ]'
ok "the key is gone on the old site after finish" '[ -z "$(wpo option get nostek_migrate_key)" ] && [ "$(PLUG -H "X-Nostek-Key: $KEY" https://old.example.test/wp-json/nostek-migrate/v1/info)" = 403 ]'
ok "the key is not in the job, the audit log or the website folder" '! grep -rqF "$KEY" "$SITE01_TEST_ROOT/var/log/nostek-agent" "$SM/site.json" "$SM/compose.yml" && ! grep -qF "$KEY" <<<"$JOB"'

api GET /v1/sites/m-example-test/backups
BM="$(python3 -c 'import json,sys; print(next(b["name"] for b in json.load(sys.stdin)["backups"] if b["kind"]=="before-migrate"))' <<<"$BODY" 2>/dev/null)"
api POST "/v1/sites/m-example-test/backups/$BM/restore" '{"what":"all"}'; J="$(jobid)"
ok "the before-migrate backup brings the fresh website back" '[ -n "$BM" ] && [ "$(wait_job "$J")" = done ] && grep -q "<title>Situs M" <<<"$(site_body m-example-test m-example-test.$ZONE /)" && ! grep -q WORDPRESS_TABLE_PREFIX "$SM/compose.yml"'

# --- cPanel full backup: nested homedir.tar with two WordPress folders and mysql/<db>.sql
wpo db export - > "$T/oldwp.sql"
python3 - "$T" <<'EOF'
import io, os, sys, tarfile
T = sys.argv[1]
plain = "<?php\ndefine( 'DB_NAME', '%s' );\ndefine( 'DB_USER', 'cpuser_x' );\ndefine( 'DB_PASSWORD', 'cpanel-old-pass' );\ndefine( 'DB_HOST', 'localhost' );\n$table_prefix = '%s';\nif ( ! defined( 'ABSPATH' ) ) { define( 'ABSPATH', __DIR__ . '/' ); }\nrequire_once ABSPATH . 'wp-settings.php';\n"
def add_tree(t, src, arc, cfg):
    for root, dirs, files in os.walk(src):
        dirs[:] = [d for d in dirs if not (root == src + "/wp-content" and d == "uploads")]
        for n in dirs + files:
            p = os.path.join(root, n); a = arc + "/" + os.path.relpath(p, src)
            if a.endswith("/wp-config.php"):
                b = cfg.encode(); i = tarfile.TarInfo(a); i.size = len(b); i.mode = 0o640; t.addfile(i, io.BytesIO(b))
            elif os.path.isdir(p) and not os.path.islink(p):
                t.addfile(t.gettarinfo(p, a))
            elif os.path.islink(p) or os.path.isfile(p):
                t.add(p, a, recursive=False)
home = io.BytesIO()
with tarfile.open(fileobj=home, mode="w") as h:
    add_tree(h, T + "/old", "public_html", plain % ("cpuser_wp", "np_"))
    add_tree(h, T + "/old", "addon.example.test", plain % ("cpuser_addon", "wp_"))
top = "backup-11.16.2026_10-00-00_cpuser"
with tarfile.open(T + "/cpanel-full.tar.gz", "w:gz") as t:
    def put(name, data):
        i = tarfile.TarInfo(top + "/" + name); i.size = len(data); i.mode = 0o600; t.addfile(i, io.BytesIO(data))
    put("homedir.tar", home.getvalue())
    put("mysql/cpuser_wp.sql", open(T + "/oldwp.sql", "rb").read())
    put("mysql/cpuser_addon.sql", b"-- another site\n")
    put("mysql.sql", b"-- grants\n")
with tarfile.open(T + "/cpanel-home.tar.gz", "w:gz") as t:
    add_tree(t, T + "/old", "public_html", plain % ("cpuser_wp", "np_"))
EOF
gzip -c "$T/oldwp.sql" > "$T/cpuser_wp.sql.gz"
raw PUT "/v1/sites/m-example-test/migrate/upload?part=archive" "$T/cpanel-full.tar.gz"
ok "cPanel full backup uploaded" '[ "$CODE" = 200 ]'
api POST /v1/sites/m-example-test/migrate '{"way":"backup"}'; J="$(jobid)"
ok "two WordPress folders: the job asks which one, before any change" '[ "$(wait_job "$J")" = failed ] && grep -q "choose one: backup-11.16.2026_10-00-00_cpuser/homedir/addon.example.test, backup-11.16.2026_10-00-00_cpuser/homedir/public_html" <<<"$JOB" && grep -q "<title>Situs M" <<<"$(site_body m-example-test m-example-test.$ZONE /)"'
api POST /v1/sites/m-example-test/migrate '{"way":"backup","root":"backup-11.16.2026_10-00-00_cpuser/homedir/public_html"}'; J="$(jobid)"
ok "cPanel full backup moved with the chosen folder and its database" '[ "$(wait_job "$J" 400)" = done ] && grep -q "<title>Situs Lama" <<<"$(site_body m-example-test m-example-test.$ZONE /)" && [ "$(cli m-example-test "option get home")" = "https://m-example-test.$ZONE" ] && grep -q "WORDPRESS_TABLE_PREFIX: np_" "$SM/compose.yml"'
ok "the uploaded backup is removed after the job" '[ ! -e "$SM/migrate/archive" ]'

# --- cPanel home backup from a download link, the database backup uploaded beside it
cp "$T/cpanel-home.tar.gz" "$T/old/home-backup.tar.gz"; chown 33:33 "$T/old/home-backup.tar.gz"
api POST "/v1/sites/m-example-test/backups/$BM/restore" '{"what":"all"}'; J="$(jobid)"; wait_job "$J" >/dev/null
raw PUT "/v1/sites/m-example-test/migrate/upload?part=database" "$T/cpuser_wp.sql.gz"
api POST /v1/sites/m-example-test/migrate '{"way":"backup","link":"https://old.example.test/home-backup.tar.gz"}'; J="$(jobid)"
ok "home backup fetched by link and moved with the separate .sql.gz" '[ "$(wait_job "$J" 400)" = done ] && grep -q "<title>Situs Lama" <<<"$(site_body m-example-test m-example-test.$ZONE /)"'
api POST /v1/sites/m-example-test/migrate '{"way":"backup","link":"http://old.example.test/home-backup.tar.gz"}'
ok "a plain http link is refused" '[ "$CODE" = 400 ]'
api POST /v1/sites/m-example-test/migrate '{"way":"ssh"}'
ok "an unknown way is refused" '[ "$CODE" = 400 ]'

cleanup; TMP_ALL="$(mktemp -d)"
LEFT="$(docker ps -aq --filter name=np-site- --filter name=np-mig-old | wc -l)"
if [ "$BAD" = 0 ] && [ "$LEFT" = 0 ]; then echo "RESULT MIG-01 HARNESS PASS tests=$TESTS sisa_uji=0"; exit 0; fi
echo "RESULT MIG-01 HARNESS FAIL bad=$BAD/$TESTS sisa_uji=$LEFT"; exit 1
