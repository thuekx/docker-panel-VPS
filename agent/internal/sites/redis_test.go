package sites

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

func redisSite(app, server string) Site {
	s := Site{ID: "voltix-id", Domain: "voltix.id", Server: server, PHP: "8.4", App: app,
		Hosts: []Host{{Name: "voltix.id", Role: "primary", Routed: true}}, Redis: &RedisConf{Mode: "cache", MB: 128}}
	if app == spec.AppNode {
		s.Server, s.PHP, s.Version, s.Release = "", "", "24", 1
	}
	return s
}

func svcBlock(cf, svc string) string {
	var b strings.Builder
	eachService(cf, func(_ int, name, l string) {
		if name == svc {
			b.WriteString(l + "\n")
		}
	})
	return b.String()
}

func TestRedisCompose(t *testing.T) {
	for _, c := range []struct {
		app, server string
		clients     []string
		env         bool
	}{
		{spec.AppWordPress, "nginx", []string{"php", "cli"}, false},
		{spec.AppWordPress, "apache", []string{"web", "cli"}, false},
		{spec.AppLaravel, "nginx", []string{"php", "queue", "scheduler"}, true},
		{spec.AppPHP, "nginx", []string{"php"}, true},
		{spec.AppPHP, "apache", []string{"web"}, true},
		{spec.AppNode, "", []string{"app"}, false},
	} {
		s := redisSite(c.app, c.server)
		if c.app == spec.AppWordPress || c.app == spec.AppLaravel {
			s.Database = true
		}
		cf, err := s.Compose()
		if err != nil {
			t.Fatal(c.app, err)
		}
		if !strings.Contains(cf, "  redis_password:\n    file: ./secrets/redis_password\n") || !strings.Contains(cf, "image: "+catalog.Redis) {
			t.Fatalf("%s/%s: no redis secret or service:\n%s", c.app, c.server, cf)
		}
		rb := svcBlock(cf, "redis")
		if !strings.Contains(rb, "networks: [back]") || !strings.Contains(rb, "tmpfs: [/data]") || !strings.Contains(rb, "mem_limit: 192m") || strings.Contains(rb, "nostek-edge") {
			t.Fatalf("%s: redis service:\n%s", c.app, rb)
		}
		for _, svc := range c.clients {
			b := svcBlock(cf, svc)
			if !strings.Contains(b, "back") {
				t.Errorf("%s/%s: %s not on back:\n%s", c.app, c.server, svc, b)
			}
			if c.app != spec.AppNode && !strings.Contains(b, "redis_password") {
				t.Errorf("%s/%s: %s lacks the secret:\n%s", c.app, c.server, svc, b)
			}
			if c.env != strings.Contains(b, "REDIS_PASSWORD_FILE: /run/secrets/redis_password") {
				t.Errorf("%s/%s: %s env %v:\n%s", c.app, c.server, svc, c.env, b)
			}
			if strings.Count(b, "secrets:") > 1 {
				t.Errorf("%s: two secrets lines in %s", c.app, svc)
			}
		}
		if c.app == spec.AppLaravel && !strings.Contains(svcBlock(cf, "php"), "REDIS_CLIENT: predis") {
			t.Error("Laravel without predis")
		}
		if strings.Contains(svcBlock(cf, "db"), "redis") {
			t.Errorf("%s: the database got redis:\n%s", c.app, svcBlock(cf, "db"))
		}
		// RAM limits leave Redis out
		for _, r := range serviceRAM(cf) {
			if r.Service == "redis" {
				t.Fatal("redis in the RAM list")
			}
		}
		s.Redis = nil
		plain, _ := s.Compose()
		if strings.Contains(plain, "redis") {
			t.Fatalf("%s: redis without Redis", c.app)
		}
	}
	s := redisSite(spec.AppWordPress, "nginx")
	s.Database, s.Redis.Mode = true, "store"
	cf, _ := s.Compose()
	if rb := svcBlock(cf, "redis"); !strings.Contains(rb, "- ./redis:/data") || strings.Contains(rb, "tmpfs") {
		t.Fatalf("store:\n%s", rb)
	}
	conf := s.redisConfig("pw123")
	for _, want := range []string{"requirepass pw123", "maxmemory 128mb", "maxmemory-policy noeviction", "appendonly yes", `rename-command CONFIG ""`} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf lacks %q", want)
		}
	}
	s.Redis.Mode = "cache"
	if c := s.redisConfig("x"); !strings.Contains(c, "allkeys-lru") || !strings.Contains(c, "appendonly no") {
		t.Fatal(c)
	}
}

const fakeRedisWP = `#!/bin/sh
echo "$*" >> "$NP_SEEN"
case "$1 $2" in
"config delete") [ "$3" = WP_REDIS_PREFIX ] && { echo "Error: The constant 'WP_REDIS_PREFIX' is not defined in the 'wp-config.php' file." >&2; exit 1; } ;;
esac
echo "Success: $1"
`

func redisWPSite(t *testing.T) (*Manager, *fakeRunner, string, string) {
	t.Helper()
	m, f, id := readySite(t)
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "wp"), []byte(fakeRedisWP), 0o755)
	seen := filepath.Join(t.TempDir(), "seen")
	f.hook = func(args []string) ([]byte, error, bool) {
		j := strings.Join(args, " ")
		if strings.Contains(j, "exec -T redis") {
			if strings.HasSuffix(j, "FLUSHALL") {
				return []byte("OK\n"), nil, true
			}
			return []byte("# Memory\r\nused_memory:1048576\r\n# Stats\r\nkeyspace_hits:90\r\nkeyspace_misses:10\r\n# Keyspace\r\ndb0:keys=12,expires=3,avg_ttl=0\r\n"), nil, true
		}
		i := -1
		for k, a := range args {
			if a == "cli" && k+3 < len(args) && args[k+1] == "sh" && args[k+2] == "-c" {
				i = k
			}
		}
		if i < 0 {
			return nil, nil, false
		}
		script := strings.Replace(args[i+3], "cd /var/www/html || exit 1", "true", 1)
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "NP_SEEN=" + seen}
		out, err := cmd.CombinedOutput()
		return out, err, true
	}
	return m, f, id, seen
}

func TestRedisWordPress(t *testing.T) {
	m, f, id, seen := redisWPSite(t)
	ctx := context.Background()
	if _, err := m.StartRedis(id, &RedisConf{Mode: "fast", MB: 128}); !errors.Is(err, ErrBadInput) {
		t.Fatal(err)
	}
	if _, err := m.StartRedis(id, &RedisConf{Mode: "cache", MB: 100}); !errors.Is(err, ErrBadInput) {
		t.Fatal(err)
	}
	if _, err := m.StartRedis(id, nil); !errors.Is(err, ErrBadInput) {
		t.Fatal("off while off", err)
	}
	job, err := m.StartRedis(id, &RedisConf{Mode: "cache", MB: 128})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	s, _ := m.Get(id)
	if s.Redis == nil || s.Redis.MB != 128 {
		t.Fatalf("%+v", s.Redis)
	}
	dir := m.dir(id)
	pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "redis_password"))
	conf, _ := os.ReadFile(filepath.Join(dir, "secrets", "redis.conf"))
	if len(pw) < 20 || !strings.Contains(string(conf), "requirepass "+string(pw)) {
		t.Fatal("password or conf")
	}
	cf, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if strings.Contains(string(cf), string(pw)) {
		t.Fatal("password in compose.yml")
	}
	log, _ := os.ReadFile(seen)
	for _, want := range []string{"config set WP_REDIS_HOST redis --type=constant", "config set WP_REDIS_PASSWORD " + wpRedisPass + " --raw", "plugin install redis-cache --activate", "redis enable --force", "WP_REDIS_PREFIX voltix-id:"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("wp lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(string(log), string(pw)) {
		t.Fatal("password on a WP-CLI command line")
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), string(pw)) {
			t.Fatalf("password in a docker call: %v", c)
		}
	}
	v, err := m.RedisStatus(ctx, id)
	if err != nil || !v.Running || v.Keys != 12 || v.Hits != 90 || v.Used != 1<<20 || v.Kind != "wordpress" {
		t.Fatalf("%+v %v", v, err)
	}
	if err := m.FlushRedis(ctx, id); err != nil {
		t.Fatal(err)
	}
	// a change of size keeps the password and does not run WP-CLI again
	_ = os.Remove(seen)
	job, _ = m.StartRedis(id, &RedisConf{Mode: "store", MB: 256})
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	pw2, _ := os.ReadFile(filepath.Join(dir, "secrets", "redis_password"))
	if string(pw2) != string(pw) {
		t.Fatal("password changed")
	}
	if _, err := os.Stat(seen); err == nil {
		t.Fatal("WP-CLI ran for a size change")
	}
	if fi, err := os.Stat(filepath.Join(dir, "redis")); err != nil || !fi.IsDir() {
		t.Fatal("no data folder for store")
	}
	if _, err := m.StartRedis(id, &RedisConf{Mode: "store", MB: 256}); !errors.Is(err, ErrBadInput) {
		t.Fatal("same again", err)
	}
	// off: WordPress first (a constant already gone is fine), then the stack, then the files
	job, _ = m.StartRedis(id, nil)
	if v := waitJob(t, m, job); v.State != "done" {
		t.Fatalf("%+v", v)
	}
	log, _ = os.ReadFile(seen)
	if !strings.Contains(string(log), "redis disable") || !strings.Contains(string(log), "config delete WP_REDIS_PASSWORD") {
		t.Fatalf("%s", log)
	}
	s, _ = m.Get(id)
	if s.Redis != nil {
		t.Fatal("still on")
	}
	for _, p := range []string{"secrets/redis_password", "secrets/redis.conf", "redis"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			t.Fatalf("%s left", p)
		}
	}
	cf, _ = os.ReadFile(filepath.Join(dir, "compose.yml"))
	if strings.Contains(string(cf), "redis") {
		t.Fatal("compose still has redis")
	}
}

func TestRedisStartFailsBack(t *testing.T) {
	m, f, id := readySite(t)
	before, _ := os.ReadFile(filepath.Join(m.dir(id), "compose.yml"))
	n := 0
	f.hook = func(args []string) ([]byte, error, bool) {
		if strings.Contains(strings.Join(args, " "), "up -d --remove-orphans") {
			n++
			if n == 1 {
				return []byte("redis unhealthy"), errors.New("exit 1"), true
			}
		}
		return nil, nil, false
	}
	job, _ := m.StartRedis(id, &RedisConf{Mode: "cache", MB: 64})
	v := waitJob(t, m, job)
	if v.State != "failed" || !strings.Contains(v.Error, "runs as before") {
		t.Fatalf("%+v", v)
	}
	after, _ := os.ReadFile(filepath.Join(m.dir(id), "compose.yml"))
	if string(after) != string(before) {
		t.Fatal("compose not put back")
	}
	if s, _ := m.Get(id); s.Redis != nil {
		t.Fatal("site.json not put back")
	}
}

func TestRedisNotForNative(t *testing.T) {
	if redisApp(Site{App: spec.AppStatic, Server: "nginx"}) || redisApp(Site{App: spec.AppUptimeKuma}) || redisApp(Site{App: spec.AppWordPress, Engine: "native"}) {
		t.Fatal("redis offered where it cannot run")
	}
	if !redisApp(Site{App: spec.AppPython}) {
		t.Fatal("python")
	}
}

func TestRedisRuntimeEnv(t *testing.T) {
	m, _ := newTest(t, nil)
	id := "shop-id"
	_ = m.writeEnv(id, map[string]string{"KEEP": "1", "REDIS_URL": "old"})
	if err := m.redisAppEnv(id, "pw"); err != nil {
		t.Fatal(err)
	}
	env, _ := readEnv(m.envPath(id))
	if env["REDIS_URL"] != "redis://:pw@redis:6379/0" || env["REDIS_PASSWORD"] != "pw" || env["KEEP"] != "1" {
		t.Fatalf("%v", env)
	}
	_ = m.redisAppEnv(id, "")
	env, _ = readEnv(m.envPath(id))
	if len(env) != 1 || env["KEEP"] != "1" {
		t.Fatalf("%v", env)
	}
}
