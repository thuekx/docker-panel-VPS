package sites

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// Host is one name a site answers on.
type Host struct {
	Name   string `json:"name"`
	Role   string `json:"role"`   // primary | www | alias | preview
	Routed bool   `json:"routed"` // true when DNS pointed here at create time and the edge serves it
}

// Site is what the agent stores in site.json. It never holds a secret.
type Site struct {
	ID         string `json:"id"`
	Domain     string `json:"domain"`
	Primary    string `json:"primary"`
	Server     string `json:"server"`
	PHP        string `json:"php"`
	AdminEmail string `json:"admin_email"`
	AdminUser  string `json:"admin_user"`
	Title      string `json:"title"`
	Hosts      []Host `json:"hosts"`
	URL        string `json:"url"` // the WordPress address (https://<host>)
	Created    int64  `json:"created"`
	Phase      string `json:"phase"`                 // creating | ready | failed
	SystemCron bool   `json:"system_cron,omitempty"` // WP-01: the agent runs wp-cron.php every 5 minutes
	// BAK-01: daily backup at 02:00 WIB and how many daily backups are kept (0 = KeepDefault)
	BackupDaily bool `json:"backup_daily,omitempty"`
	BackupKeep  int  `json:"backup_keep,omitempty"`
	// APP-01: what the website runs; empty = WordPress (every site made before v1.8.0)
	App      string `json:"app,omitempty"`
	Database bool   `json:"database,omitempty"`  // php, node, go, python: the app has its own database
	DBEngine string `json:"db_engine,omitempty"` // "" = MariaDB, postgres = PostgreSQL (APP-02b)
	Version  string `json:"version,omitempty"`   // laravel: major version installed at create time; node: Node.js
	// APP-02 (node): where the source comes from, how it is built and started, and the live release
	Source  string `json:"source,omitempty"` // git (empty) | files
	Dir     string `json:"dir,omitempty"`    // app folder inside the repository or source folder
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Build   string `json:"build,omitempty"`
	Start   string `json:"start,omitempty"`
	Release int    `json:"release,omitempty"` // live release number (0 = none yet)

	keyID string // deploy key made before the website (SSH repository), moved in by the create job
}

// IsRuntime is true for an app built from source: Node.js, Go or Python.
func (s Site) IsRuntime() bool { return spec.IsRuntimeApp(s.AppName()) }

// FromFiles is true when a Node.js app is built from the website's source/ folder instead of Git.
func (s Site) FromFiles() bool { return s.Source == spec.SourceFiles }

// NodePort is the port a Node.js app listens on (it gets PORT in its environment).
const NodePort = 3000

// BuildCmd and StartCmd return the commands of a Node.js app, defaults when none was set.
func (s Site) BuildCmd() string {
	if s.Build == "" {
		return spec.DefaultBuild[s.AppName()]
	}
	return s.Build
}

// StartCmd returns the start command of a Node.js app.
func (s Site) StartCmd() string {
	if s.Start == "" {
		return spec.DefaultStart[s.AppName()]
	}
	return s.Start
}

// AppName is the app the site runs: wordpress, php or static.
func (s Site) AppName() string {
	if s.App == "" {
		return spec.AppWordPress
	}
	return s.App
}

// IsWP is true for a WordPress site.
func (s Site) IsWP() bool { return s.AppName() == spec.AppWordPress }

// IsLaravel is true for a Laravel site.
func (s Site) IsLaravel() bool { return s.AppName() == spec.AppLaravel }

// IsPostgres is true when the site's own database is PostgreSQL instead of MariaDB.
func (s Site) IsPostgres() bool { return s.HasDB() && s.DBEngine == spec.DBPostgres }

// HasDB is true when the site has its own database (MariaDB, or PostgreSQL for an app built from source).
func (s Site) HasDB() bool {
	return s.IsWP() || s.IsLaravel() || ((s.AppName() == spec.AppPHP || s.IsRuntime()) && s.Database)
}

// DBName is the database (and its user) of the site.
func (s Site) DBName() string {
	if s.IsWP() {
		return "wordpress"
	}
	return "app"
}

// Project is the compose project name of a site.
func Project(id string) string { return "np-site-" + id }

// Canonical returns the host WordPress uses: the primary name when routed, else the preview name.
func (s Site) Canonical() string {
	for _, role := range []string{"primary", "preview"} {
		for _, h := range s.Hosts {
			if h.Role == role && h.Routed {
				return h.Name
			}
		}
	}
	return ""
}

var routerSafe = regexp.MustCompile(`[^a-z0-9-]`)

// labels returns the Traefik labels: the canonical host is served, every other routed host redirects to it.
// One router per host, so each name gets its own certificate and one failing name never blocks the others.
func (s Site) labels() []string {
	port := 80
	if s.IsRuntime() {
		port = NodePort
	}
	canon := s.Canonical()
	rid := "site-" + s.ID
	l := []string{
		`traefik.enable: "true"`,
		`traefik.docker.network: nostek-edge`,
		fmt.Sprintf(`traefik.http.services.%s.loadbalancer.server.port: "%d"`, rid, port),
		fmt.Sprintf(`traefik.http.middlewares.%s-canon.redirectregex.regex: "^https?://[^/]+(.*)"`, rid),
		fmt.Sprintf(`traefik.http.middlewares.%s-canon.redirectregex.replacement: "https://%s$${1}"`, rid, canon),
		fmt.Sprintf(`traefik.http.middlewares.%s-canon.redirectregex.permanent: "true"`, rid),
	}
	for _, h := range s.Hosts {
		if !h.Routed {
			continue
		}
		r := rid + "-" + routerSafe.ReplaceAllString(strings.ReplaceAll(h.Name, ".", "-"), "")
		l = append(l,
			fmt.Sprintf("traefik.http.routers.%s.rule: Host(`%s`)", r, h.Name),
			fmt.Sprintf(`traefik.http.routers.%s.entrypoints: websecure`, r),
			fmt.Sprintf(`traefik.http.routers.%s.tls.certresolver: le`, r),
			fmt.Sprintf(`traefik.http.routers.%s.service: %s`, r, rid),
		)
		if h.Name != canon {
			l = append(l, fmt.Sprintf(`traefik.http.routers.%s.middlewares: %s-canon`, r, rid))
		}
	}
	return l
}

func indent(lines []string, n int) string {
	p := strings.Repeat(" ", n)
	return p + strings.Join(lines, "\n"+p)
}

// wpConfigExtra trusts the edge's X-Forwarded-Proto (the only way in), forces HTTPS for the admin and turns off
// the in-browser code editor. `$$` is compose's escape for a literal `$`.
const wpConfigExtra = `WORDPRESS_CONFIG_EXTRA: "define('FORCE_SSL_ADMIN', true); if (isset($$_SERVER['HTTP_X_FORWARDED_PROTO']) && $$_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { $$_SERVER['HTTPS'] = 'on'; } define('DISALLOW_FILE_EDIT', true);"`

const wpHealth = `    healthcheck:
      test: ["CMD", "test", "-f", "/var/www/html/wp-config.php"]
      interval: 5s
      timeout: 3s
      retries: 60
`

// Compose renders the site's compose file. Every value in it comes from checked input or the catalogue.
func (s Site) Compose() (string, error) {
	if s.Canonical() == "" {
		return "", fmt.Errorf("site %s has no routed host", s.ID)
	}
	var p catalog.PHPVersion
	switch s.AppName() {
	case spec.AppWordPress, spec.AppPHP:
		var ok bool
		if p, ok = catalog.PHP(s.PHP); !ok {
			return "", fmt.Errorf("unknown PHP %s", s.PHP)
		}
		if s.Server != "apache" && s.Server != "nginx" {
			return "", fmt.Errorf("unknown web server %s", s.Server)
		}
	case spec.AppLaravel:
		var ok bool
		if p, ok = catalog.PHP(s.PHP); !ok {
			return "", fmt.Errorf("unknown PHP %s", s.PHP)
		}
		if s.Server != "nginx" {
			return "", fmt.Errorf("a Laravel site runs on nginx")
		}
	case spec.AppStatic:
		if s.Server != "nginx" {
			return "", fmt.Errorf("a static site runs on nginx")
		}
	case spec.AppNode, spec.AppGo, spec.AppPython:
		if _, ok := catalog.RuntimeOf(s.AppName(), s.Version); !ok {
			return "", fmt.Errorf("unknown %s %s", s.AppName(), s.Version)
		}
		if s.Release < 1 {
			return "", fmt.Errorf("site %s has no release yet", s.ID)
		}
	default:
		return "", fmt.Errorf("unknown app %s", s.App)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Generated by nostek-agent for site %s. Do not edit: the agent rewrites it.
name: %s

x-site: &site
  restart: unless-stopped
  security_opt: ["no-new-privileges:true"]
  cap_drop: [NET_RAW, MKNOD, AUDIT_WRITE, SYS_CHROOT, SETFCAP, SETPCAP]
  pids_limit: 512
  logging:
    driver: json-file
    options: { max-size: "10m", max-file: "3" }
`, s.ID, Project(s.ID))
	if s.HasDB() {
		fmt.Fprintf(&b, `
secrets:
  db_password:
    file: ./secrets/db_password
`)
	}
	b.WriteString("\nservices:\n")
	if s.IsPostgres() {
		fmt.Fprintf(&b, `  db:
    <<: *site
    image: %s
    mem_limit: 512m
    shm_size: 128m
    environment:
      POSTGRES_DB: %s
      POSTGRES_USER: %s
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
    secrets: [db_password]
    volumes:
      - ./db:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -q -h 127.0.0.1 -U %s -d %s"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 20s
    networks: [back]

`, catalog.Postgres, s.DBName(), s.DBName(), s.DBName(), s.DBName())
	} else if s.HasDB() {
		fmt.Fprintf(&b, `  db:
    <<: *site
    image: %s
    mem_limit: 512m
    environment:
      MARIADB_DATABASE: %s
      MARIADB_USER: %s
      MARIADB_PASSWORD_FILE: /run/secrets/db_password
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
      MARIADB_AUTO_UPGRADE: "1"
    secrets: [db_password]
    volumes:
      - ./db:/var/lib/mysql
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 20s
    networks: [back]

`, catalog.MariaDB, s.DBName(), s.DBName())
	}
	switch s.AppName() {
	case spec.AppWordPress:
		s.wpServices(&b, p)
	case spec.AppPHP:
		s.phpServices(&b, p)
	case spec.AppLaravel:
		s.laravelServices(&b, p)
	case spec.AppNode, spec.AppGo, spec.AppPython:
		s.nodeServices(&b)
	case spec.AppStatic:
		fmt.Fprintf(&b, `  web:
    <<: *site
    image: %s
    mem_limit: 128m
    volumes:
      - ./wp:/var/www/html:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1/.nostek-health"]
      interval: 5s
      timeout: 3s
      retries: 60
    labels:
%s
    networks: [nostek-edge]
`, catalog.Nginx, indent(s.labels(), 6))
	}
	b.WriteString(`
networks:
  back:
    internal: true
  nostek-edge:
    external: true
  out: {}
`)
	return b.String(), nil
}

func (s Site) wpServices(b *strings.Builder, p catalog.PHPVersion) {
	wpEnv := indent([]string{
		"WORDPRESS_DB_HOST: db",
		"WORDPRESS_DB_NAME: wordpress",
		"WORDPRESS_DB_USER: wordpress",
		"WORDPRESS_DB_PASSWORD_FILE: /run/secrets/db_password",
		wpConfigExtra,
	}, 6)
	if s.Server == "apache" {
		fmt.Fprintf(b, `  web:
    <<: *site
    image: %s
    mem_limit: 512m
    depends_on:
      db: { condition: service_healthy }
    environment:
%s
    secrets: [db_password]
    volumes:
      - ./wp:/var/www/html
      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro
%s    labels:
%s
    networks: [back, nostek-edge]
`, p.Apache, wpEnv, wpHealth, indent(s.labels(), 6))
	} else {
		fmt.Fprintf(b, `  php:
    <<: *site
    image: %s
    mem_limit: 512m
    depends_on:
      db: { condition: service_healthy }
    environment:
%s
    secrets: [db_password]
    volumes:
      - ./wp:/var/www/html
      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro
%s    networks: [back, out]

  web:
    <<: *site
    image: %s
    mem_limit: 128m
    depends_on:
      php: { condition: service_healthy }
    volumes:
      - ./wp:/var/www/html:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    labels:
%s
    networks: [back, nostek-edge]
`, p.FPM, wpEnv, wpHealth, catalog.Nginx, indent(s.labels(), 6))
	}
	fmt.Fprintf(b, `
  cli:
    image: %s
    profiles: [tools]
    user: "33:33"
    security_opt: ["no-new-privileges:true"]
    environment:
%s
    secrets: [db_password]
    volumes:
      - ./wp:/var/www/html
    networks: [back, out]
`, p.CLI, wpEnv)
}

// phpHealth only checks that the code folder is mounted: a plain PHP app may answer anything on "/".
const phpHealth = `    healthcheck:
      test: ["CMD", "test", "-d", "/var/www/html"]
      interval: 5s
      timeout: 3s
      retries: 60
`

// phpServices runs plain PHP with the same PHP images as WordPress, but through the PHP image's own entrypoint,
// so WordPress is never copied in. The database address, name and user are environment values; the password is
// a file (DB_PASSWORD_FILE), never an environment value.
func (s Site) phpServices(b *strings.Builder, p catalog.PHPVersion) {
	env, dep, sec := "", "", ""
	if s.HasDB() {
		env = "    environment:\n" + indent([]string{"DB_HOST: db", "DB_NAME: app", "DB_USER: app", "DB_PASSWORD_FILE: /run/secrets/db_password"}, 6) + "\n"
		dep = "    depends_on:\n      db: { condition: service_healthy }\n"
		sec = "    secrets: [db_password]\n"
	}
	if s.Server == "apache" {
		fmt.Fprintf(b, `  web:
    <<: *site
    image: %s
    entrypoint: ["docker-php-entrypoint"]
    command: ["apache2-foreground"]
    mem_limit: 512m
%s%s%s    volumes:
      - ./wp:/var/www/html
      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro
%s    labels:
%s
    networks: [back, nostek-edge]
`, p.Apache, dep, env, sec, phpHealth, indent(s.labels(), 6))
		return
	}
	fmt.Fprintf(b, `  php:
    <<: *site
    image: %s
    entrypoint: ["docker-php-entrypoint"]
    command: ["php-fpm"]
    mem_limit: 512m
%s%s%s    volumes:
      - ./wp:/var/www/html
      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro
%s    networks: [back, out]

  web:
    <<: *site
    image: %s
    mem_limit: 128m
    depends_on:
      php: { condition: service_healthy }
    volumes:
      - ./wp:/var/www/html:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    labels:
%s
    networks: [back, nostek-edge]
`, p.FPM, dep, env, sec, phpHealth, catalog.Nginx, indent(s.labels(), 6))
}

// laravelServices runs Laravel: PHP-FPM on the Laravel image built from official images (catalog.LaravelDockerfile),
// Nginx serving public/, a queue worker and the scheduler. Every PHP container runs as www-data. The database
// settings are environment values that win over .env; the password is a file (DB_PASSWORD_FILE) that the prepend
// file LaravelPrepend hands to Laravel, never an environment value or a line in .env.
func (s Site) laravelServices(b *strings.Builder, p catalog.PHPVersion) {
	env := indent([]string{
		"DB_CONNECTION: mysql", "DB_HOST: db", `DB_PORT: "3306"`, "DB_DATABASE: app", "DB_USERNAME: app",
		"DB_PASSWORD_FILE: /run/secrets/db_password",
	}, 6)
	vols := `    volumes:
      - ./wp:/var/www/html
      - ./php.ini:/usr/local/etc/php/conf.d/zz-nostek.ini:ro
      - ./laravel.ini:/usr/local/etc/php/conf.d/zz-nostek-laravel.ini:ro
      - ./laravel-env.php:/usr/local/etc/php/nostek-env.php:ro
`
	img := catalog.LaravelImage(p)
	fmt.Fprintf(b, `  php:
    <<: *site
    image: %s
    build:
      context: ./build
    pull_policy: never
    entrypoint: ["docker-php-entrypoint"]
    command: ["php-fpm"]
    user: "33:33"
    mem_limit: 512m
    depends_on:
      db: { condition: service_healthy }
    environment:
%s
    secrets: [db_password]
%s%s    networks: [back, out]

  web:
    <<: *site
    image: %s
    mem_limit: 128m
    depends_on:
      php: { condition: service_healthy }
    volumes:
      - ./wp:/var/www/html:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    labels:
%s
    networks: [back, nostek-edge]
`, img, env, vols, phpHealth, catalog.Nginx, indent(s.labels(), 6))
	for _, w := range []struct{ name, cmd, mem string }{
		{"queue", `["php", "artisan", "queue:work", "--sleep=3", "--tries=3", "--max-time=3600"]`, "256m"},
		{"scheduler", `["php", "artisan", "schedule:work"]`, "256m"},
	} {
		fmt.Fprintf(b, `
  %s:
    <<: *site
    image: %s
    pull_policy: never
    entrypoint: ["docker-php-entrypoint"]
    command: %s
    user: "33:33"
    mem_limit: %s
    depends_on:
      php: { condition: service_healthy }
    environment:
%s
    secrets: [db_password]
%s    networks: [back, out]
`, w.name, img, w.cmd, w.mem, env, vols)
	}
}

// LaravelIni loads LaravelPrepend before every PHP script of a Laravel site (web, artisan, queue, scheduler).
const LaravelIni = `; Generated by nostek-agent.
auto_prepend_file = /usr/local/etc/php/nostek-env.php
`

// LaravelPrepend gives Laravel its database password from the secret file. Laravel reads $_SERVER, $_ENV and
// getenv(), all set here before Laravel starts; the value never sits in compose.yml, an environment value or .env.
const LaravelPrepend = `<?php
// Generated by nostek-agent: hands the database password from its secret file to Laravel.
if (getenv('DB_PASSWORD') === false && ($nostekFile = getenv('DB_PASSWORD_FILE')) && is_readable($nostekFile)) {
    $nostekValue = rtrim((string) file_get_contents($nostekFile), "\r\n");
    putenv('DB_PASSWORD=' . $nostekValue);
    $_ENV['DB_PASSWORD'] = $_SERVER['DB_PASSWORD'] = $nostekValue;
    unset($nostekValue);
}
unset($nostekFile);
`

// PHPIni is the per-site PHP setting file.
const PHPIni = `; Generated by nostek-agent.
upload_max_filesize = 128M
post_max_size = 128M
memory_limit = 256M
max_execution_time = 120
expose_php = Off
`

// NginxConf serves WordPress static files and passes PHP to the php service. Only the edge can reach nginx,
// so the client address is taken from the edge's X-Forwarded-For.
const NginxConf = `# Generated by nostek-agent.
server {
    listen 80;
    server_name _;
    root /var/www/html;
    index index.php;
    client_max_body_size 128m;
    server_tokens off;

    set_real_ip_from 0.0.0.0/0;
    real_ip_header X-Forwarded-For;

    location = /favicon.ico { log_not_found off; access_log off; }
    location = /robots.txt  { log_not_found off; access_log off; try_files $uri /index.php?$args; }
    location ~ /\.(?!well-known) { deny all; }
    location ~* /(?:uploads|files)/.*\.php$ { deny all; }
    location = /wp-config.php { deny all; }

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_pass php:9000;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 120s;
    }

    location ~* \.(?:css|js|jpe?g|png|gif|webp|avif|svg|ico|woff2?)$ {
        expires 30d;
        access_log off;
        try_files $uri /index.php?$args;
    }
}
`

// NginxPHPConf serves a plain PHP app: files first, then index.php (front controllers such as Slim or a router).
const NginxPHPConf = `# Generated by nostek-agent.
server {
    listen 80;
    server_name _;
    root /var/www/html;
    index index.php index.html;
    client_max_body_size 128m;
    server_tokens off;

    set_real_ip_from 0.0.0.0/0;
    real_ip_header X-Forwarded-For;

    location ~ /\.(?!well-known) { deny all; }

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_pass php:9000;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 120s;
    }
}
`

// NginxLaravelConf serves a Laravel app: the web root is public/, so .env, vendor/ and storage/ are never served.
const NginxLaravelConf = `# Generated by nostek-agent.
server {
    listen 80;
    server_name _;
    root /var/www/html/public;
    index index.php;
    client_max_body_size 128m;
    server_tokens off;

    set_real_ip_from 0.0.0.0/0;
    real_ip_header X-Forwarded-For;

    location ~ /\.(?!well-known) { deny all; }

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ ^/index\.php(/|$) {
        fastcgi_split_path_info ^(.+\.php)(/.+)$;
        fastcgi_pass php:9000;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        fastcgi_param DOCUMENT_ROOT $realpath_root;
        fastcgi_param PATH_INFO $fastcgi_path_info;
        fastcgi_param HTTPS on;
        fastcgi_read_timeout 120s;
    }

    location ~ \.php$ { return 404; }
}
`

// NginxStaticConf serves a static site. Unknown paths fall back to index.html, so single-page apps (React, Vue,
// Svelte) work with their own routes; hashed assets are cached long.
const NginxStaticConf = `# Generated by nostek-agent.
server {
    listen 80;
    server_name _;
    root /var/www/html;
    index index.html;
    server_tokens off;

    location = /.nostek-health { access_log off; default_type text/plain; return 200 "ok"; }
    location ~ /\.(?!well-known) { deny all; }

    location / {
        try_files $uri $uri/ /index.html;
    }

    location ~* \.(?:css|js|mjs|jpe?g|png|gif|webp|avif|svg|ico|woff2?)$ {
        expires 30d;
        access_log off;
        try_files $uri =404;
    }
}
`

// nodeServices renders the app container of a Node.js website: the official Node.js image runs the live release
// as www-data, with PORT set, reachable only through the edge; it may reach the internet (APIs, mail).
func (s Site) nodeServices(b *strings.Builder) {
	rt, _ := catalog.RuntimeOf(s.AppName(), s.Version)
	env := "      NODE_ENV: production\n      NPM_CONFIG_UPDATE_NOTIFIER: \"false\"\n"
	health := fmt.Sprintf(`["CMD", "node", "-e", "require('http').get('http://127.0.0.1:%d/',r=>process.exit(r.statusCode<500?0:1)).on('error',()=>process.exit(1))"]`, NodePort)
	if s.AppName() != spec.AppNode {
		env = "      PYTHONUNBUFFERED: \"1\"\n      GIN_MODE: release\n"
		health = fmt.Sprintf(`["CMD-SHELL", "c=$$(curl -s -o /dev/null -w '%%{http_code}' --max-time 3 http://127.0.0.1:%d/); [ $${c:-0} -gt 0 ] && [ $${c:-0} -lt 500 ]"]`, NodePort)
	}
	nets, dep := "[nostek-edge, out]", ""
	if s.HasDB() {
		nets, dep = "[nostek-edge, out, back]", "    depends_on:\n      db: { condition: service_healthy }\n"
	}
	fmt.Fprintf(b, `  app:
    <<: *site
    image: %s
    user: "%d:%d"
    working_dir: /app
    command: ["sh", "-c", %s]
    mem_limit: 768m
%s    environment:
%s      PORT: "%d"
      HOST: 0.0.0.0
      HOSTNAME: 0.0.0.0
      HOME: /tmp
    env_file:
      - path: ./deploy/app.env
        required: false
        format: raw
    volumes:
      - ./wp/releases/%d:/app
    healthcheck:
      test: %s
      interval: 5s
      timeout: 4s
      retries: 3
      start_period: 120s
    labels:
%s
    networks: %s
`, rt.Image, wwwData, wwwData, yamlQuote("exec "+s.StartCmd()), dep, env, NodePort, s.Release, health, indent(s.labels(), 6), nets)
}

// yamlQuote writes a string as a YAML double-quoted scalar; `$` is doubled so compose does not expand it.
func yamlQuote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`)
	return `"` + r.Replace(v) + `"`
}
