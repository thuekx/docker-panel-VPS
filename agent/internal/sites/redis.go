package sites

// REDIS-01: a website's own Redis, from one switch.
//
// The Redis runs as one more service of the website's stack: its own container, its own password (a secret file,
// never an environment value of a PHP container or a line of compose.yml), only on the website's internal network
// (back), so no other website and nothing outside can reach it. Two modes:
//   - cache: memory only (no file on disk); the least used keys go first when the memory is full;
//   - store: also kept on disk (append-only file in the website folder); nothing is thrown away when full, so jobs
//     of a queue are never lost (a full Redis answers an error instead).
// CONFIG is switched off, so an app cannot change where Redis writes.
//
// Connection, per kind of website:
//   - WordPress: the "Redis Object Cache" plugin (redis-cache, from wordpress.org) with its own PHP client (predis),
//     so no PHP extension is needed. WP-CLI sets WP_REDIS_HOST, WP_REDIS_PASSWORD (read from the secret file at run
//     time), WP_REDIS_CLIENT and WP_REDIS_PREFIX in wp-config.php and turns on the object cache.
//   - Laravel: REDIS_HOST, REDIS_PORT and REDIS_CLIENT=predis as environment values, the password through the
//     prepend file from REDIS_PASSWORD_FILE. The app needs predis/predis (composer) and chooses what goes to Redis
//     itself (CACHE_STORE, QUEUE_CONNECTION, SESSION_DRIVER).
//   - plain PHP: REDIS_HOST, REDIS_PORT and REDIS_PASSWORD_FILE.
//   - Node.js, Go, Python: REDIS_URL, REDIS_HOST, REDIS_PORT and REDIS_PASSWORD in the app's environment file.
// Native websites, static websites and ready apps have no Redis.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thuekx/docker-panel-VPS/agent/internal/catalog"
	"github.com/thuekx/docker-panel-VPS/agent/internal/spec"
)

// RedisConf is a website's Redis.
type RedisConf struct {
	Mode string `json:"mode"` // cache | store
	MB   int    `json:"mb"`   // memory for data
}

// RedisSizes are the memory choices, in MB.
var RedisSizes = []int{64, 128, 256, 512}

const (
	redisPort     = "6379"
	redisPassFile = "redis_password"
	redisConfFile = "redis.conf"
	redisDataDir  = "redis"
)

// redisApp tells whether a website can have Redis.
func redisApp(s Site) bool {
	if s.IsNative() {
		return false
	}
	switch s.AppName() {
	case spec.AppWordPress, spec.AppPHP, spec.AppLaravel, spec.AppNode, spec.AppGo, spec.AppPython:
		return true
	}
	return false
}

// redisClientServices are the services that talk to Redis.
func (s Site) redisClientServices() []string {
	switch {
	case s.IsWP():
		if s.Server == "apache" {
			return []string{"web", "cli"}
		}
		return []string{"php", "cli"}
	case s.IsLaravel():
		return []string{"php", "queue", "scheduler"}
	case s.AppName() == spec.AppPHP:
		if s.Server == "apache" {
			return []string{"web"}
		}
		return []string{"php"}
	case s.IsRuntime():
		return []string{"app"}
	}
	return nil
}

// redisEnv are the environment values a PHP client service gets (never the password itself).
func (s Site) redisEnv() []string {
	if s.IsWP() || s.IsRuntime() {
		return nil // WordPress reads wp-config.php; runtimes read the environment file
	}
	env := []string{"REDIS_HOST: redis", `REDIS_PORT: "` + redisPort + `"`, "REDIS_PASSWORD_FILE: /run/secrets/" + redisPassFile}
	if s.IsLaravel() {
		env = append(env, "REDIS_CLIENT: predis")
	}
	return env
}

// redisClients adds the secret, the environment values and the network to the client services of a rendered
// compose file.
func (s Site) redisClients(cf string) string {
	if s.Redis == nil {
		return cf
	}
	clients := s.redisClientServices()
	lines := strings.Split(cf, "\n")
	out := make([]string, 0, len(lines)+16)
	in, svc, hasSecrets, hasEnv := false, "", false, false
	flush := func() { // before the networks line of a client service: what it lacks
		if !hasEnv && len(s.redisEnv()) > 0 {
			out = append(out, "    environment:")
			for _, e := range s.redisEnv() {
				out = append(out, "      "+e)
			}
		}
		if !hasSecrets && !s.IsRuntime() {
			out = append(out, "    secrets: ["+redisPassFile+"]")
		}
	}
	for _, l := range lines {
		switch {
		case l == "services:":
			in = true
		case l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "#"):
			in = false
		case in && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(l, ":"):
			svc, hasSecrets, hasEnv = strings.TrimSuffix(strings.TrimSpace(l), ":"), false, false
		}
		if !in || !slices.Contains(clients, svc) {
			out = append(out, l)
			continue
		}
		switch {
		case strings.HasPrefix(l, "    secrets: [") && !s.IsRuntime():
			hasSecrets = true
			l = strings.TrimSuffix(l, "]") + ", " + redisPassFile + "]"
		case strings.HasPrefix(l, "    networks: ["):
			flush()
			hasSecrets, hasEnv = true, true
			if !strings.Contains(l, "back") {
				l = strings.TrimSuffix(l, "]") + ", back]"
			}
		}
		out = append(out, l)
		if l == "    environment:" {
			hasEnv = true
			for _, e := range s.redisEnv() {
				out = append(out, "      "+e)
			}
		}
	}
	return strings.Join(out, "\n")
}

// redisService writes the redis service.
func (s Site) redisService(b *strings.Builder) {
	data := "    tmpfs: [/data]\n" // cache: memory only (and no anonymous volume for the image's /data)
	if s.Redis.Mode == "store" {
		data = "      - ./" + redisDataDir + ":/data\n"
	}
	vols := "    volumes:\n      - ./secrets/" + redisConfFile + ":/usr/local/etc/redis/redis.conf:ro\n"
	if s.Redis.Mode == "store" {
		vols += data
		data = ""
	}
	fmt.Fprintf(b, `
  redis:
    <<: *site
    image: %s
    command: ["redis-server", "/usr/local/etc/redis/redis.conf"]
    mem_limit: %dm
%s%s    secrets: [%s]
    healthcheck:
      test: ["CMD-SHELL", "REDISCLI_AUTH=\"$$(cat /run/secrets/%s)\" redis-cli ping | grep -q PONG"]
      interval: 10s
      timeout: 3s
      retries: 30
    networks: [back]
`, catalog.Redis, s.Redis.MB+64, vols, data, redisPassFile, redisPassFile)
}

// redisConfig is the redis.conf of a website (it holds the password: a secret file).
func (s Site) redisConfig(pass string) string {
	policy, persist := "allkeys-lru", "appendonly no\n"
	if s.Redis.Mode == "store" {
		policy, persist = "noeviction", "appendonly yes\nappendfsync everysec\n"
	}
	return fmt.Sprintf(`# Generated by nostek-agent for site %s. Do not edit: the agent rewrites it.
bind 0.0.0.0
port %s
requirepass %s
maxmemory %dmb
maxmemory-policy %s
save ""
%srename-command CONFIG ""
`, s.ID, redisPort, pass, s.Redis.MB, policy, persist)
}

// RedisView is the Redis of a website for the Performance page.
type RedisView struct {
	Possible bool       `json:"possible"`
	Why      string     `json:"why,omitempty"`
	Conf     *RedisConf `json:"conf,omitempty"`
	Sizes    []int      `json:"sizes"`
	Kind     string     `json:"kind"` // wordpress | laravel | php | runtime
	Running  bool       `json:"running"`
	Used     int64      `json:"used,omitempty"` // bytes
	Keys     int64      `json:"keys"`
	Hits     int64      `json:"hits"`
	Misses   int64      `json:"misses"`
	Job      *JobView   `json:"job,omitempty"`
}

func redisKind(s Site) string {
	switch {
	case s.IsWP():
		return "wordpress"
	case s.IsLaravel():
		return "laravel"
	case s.IsRuntime():
		return "runtime"
	}
	return "php"
}

// RedisStatus returns a website's Redis and, when it runs, its figures.
func (m *Manager) RedisStatus(ctx context.Context, id string) (RedisView, error) {
	s, err := m.readySite(id)
	if err != nil {
		return RedisView{}, err
	}
	v := RedisView{Possible: redisApp(s), Conf: s.Redis, Sizes: RedisSizes, Kind: redisKind(s)}
	if !v.Possible {
		v.Why = "native, static websites and ready apps have no Redis"
	}
	for _, j := range m.jobs.List() {
		if j.Kind == "redis" && j.Site == id && (v.Job == nil || j.Started.After(v.Job.Started)) {
			jv := j
			v.Job = &jv
		}
	}
	if s.Redis == nil {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := m.redisCLI(ctx, id, "INFO")
	if err != nil {
		return v, nil
	}
	v.Running = true
	for l := range strings.SplitSeq(string(out), "\n") {
		k, val, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		switch k {
		case "used_memory":
			v.Used, _ = strconv.ParseInt(val, 10, 64)
		case "keyspace_hits":
			v.Hits, _ = strconv.ParseInt(val, 10, 64)
		case "keyspace_misses":
			v.Misses, _ = strconv.ParseInt(val, 10, 64)
		default:
			if strings.HasPrefix(k, "db") { // db0:keys=12,expires=3,avg_ttl=0
				if ks, ok := strings.CutPrefix(strings.SplitN(val, ",", 2)[0], "keys="); ok {
					n, _ := strconv.ParseInt(ks, 10, 64)
					v.Keys += n
				}
			}
		}
	}
	return v, nil
}

// redisCLI runs redis-cli inside the website's redis container, with the password from its secret file.
func (m *Manager) redisCLI(ctx context.Context, id string, args ...string) ([]byte, error) {
	cmd := `REDISCLI_AUTH="$(cat /run/secrets/` + redisPassFile + `)" exec redis-cli "$@"`
	a := append([]string{"exec", "-T", "redis", "sh", "-c", cmd, "redis-cli"}, args...)
	out, err := m.compose(ctx, id, nil, a...)
	if err != nil {
		return out, fmt.Errorf("redis-cli: %v %s", err, tail(out))
	}
	return out, nil
}

// FlushRedis empties a website's Redis.
func (m *Manager) FlushRedis(ctx context.Context, id string) error {
	s, err := m.readySite(id)
	if err != nil {
		return err
	}
	if s.Redis == nil {
		return fmt.Errorf("%w: this website has no Redis", ErrBadInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := m.redisCLI(ctx, id, "FLUSHALL")
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "OK") {
		return fmt.Errorf("redis answered %s", tail(out))
	}
	return nil
}

// StartRedis switches a website's Redis on or changes it (c set), or off (c nil), as a job.
func (m *Manager) StartRedis(id string, c *RedisConf) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !redisApp(s) {
		return "", fmt.Errorf("%w: native, static websites and ready apps have no Redis", ErrBadInput)
	}
	if c != nil {
		if c.Mode != "cache" && c.Mode != "store" {
			return "", fmt.Errorf("%w: mode must be cache or store", ErrBadInput)
		}
		if !slices.Contains(RedisSizes, c.MB) {
			return "", fmt.Errorf("%w: memory must be one of %v MB", ErrBadInput, RedisSizes)
		}
		if s.Redis != nil && *s.Redis == *c {
			return "", fmt.Errorf("%w: nothing changes", ErrBadInput)
		}
	} else if s.Redis == nil {
		return "", fmt.Errorf("%w: this website has no Redis", ErrBadInput)
	}
	if s.IsRuntime() && s.Release < 1 {
		return "", fmt.Errorf("%w: deploy the app once first", ErrBadInput)
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	j := m.jobs.New("redis", id)
	go func() {
		defer m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		var err error
		if c == nil {
			err = m.redisOff(ctx, j, s)
		} else {
			err = m.redisOn(ctx, j, s, *c)
		}
		if err != nil {
			j.Fail(err.Error())
			return
		}
		j.Done("")
	}()
	return j.ID, nil
}

// redisStack writes compose.yml and site.json for next and starts the stack; on a failure the files of before
// come back and the website is started on them again.
func (m *Manager) redisStack(ctx context.Context, prev, next Site) error {
	dir := m.dir(next.ID)
	old, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	cf, err := next.Compose()
	if err != nil {
		return err
	}
	cf = keepImages(cf, string(old), "redis")
	if err := writeFile(filepath.Join(dir, "compose.yml"), []byte(cf), 0o644); err != nil {
		return err
	}
	if err := writeSite(dir, next); err != nil {
		_ = writeFile(filepath.Join(dir, "compose.yml"), old, 0o644)
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	out, err := m.compose(sctx, next.ID, nil, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", fmt.Sprint(StartTimeout))
	if err == nil {
		return nil
	}
	_ = writeFile(filepath.Join(dir, "compose.yml"), old, 0o644)
	_ = writeSite(dir, prev)
	if out2, err2 := m.compose(ctx, next.ID, nil, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", fmt.Sprint(StartTimeout)); err2 != nil {
		return fmt.Errorf("the website did not start with Redis (%s), nor without it: %s", tail(out), tail(out2))
	}
	return fmt.Errorf("the website did not start with Redis; it runs as before: %s", tail(out))
}

func (m *Manager) redisOn(ctx context.Context, j *Job, s Site, c RedisConf) error {
	dir := m.dir(s.ID)
	next := s
	next.Redis = &c
	j.Step("files")
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		return err
	}
	pf := filepath.Join(dir, "secrets", redisPassFile)
	pw, err := os.ReadFile(pf)
	if err != nil || len(strings.TrimSpace(string(pw))) < 20 {
		pw = []byte(Password())
		// read by the website's own containers only through their mounts; the folder above is 0700
		if err := writeFile(pf, pw, 0o444); err != nil {
			return err
		}
	}
	pass := strings.TrimSpace(string(pw))
	if err := writeFile(filepath.Join(dir, "secrets", redisConfFile), []byte(next.redisConfig(pass)), 0o444); err != nil {
		return err
	}
	if c.Mode == "store" {
		if err := os.MkdirAll(filepath.Join(dir, redisDataDir), 0o755); err != nil {
			return err
		}
	}
	if s.IsLaravel() {
		if err := writeFile(filepath.Join(dir, "laravel-env.php"), []byte(LaravelPrepend), 0o644); err != nil {
			return err
		}
	}
	if s.IsRuntime() {
		if err := m.redisAppEnv(s.ID, pass); err != nil {
			return err
		}
	}
	j.Step("start redis")
	if err := m.redisStack(ctx, s, next); err != nil {
		if s.IsRuntime() && s.Redis == nil {
			_ = m.redisAppEnv(s.ID, "")
		}
		return err
	}
	if c.Mode == "cache" {
		_ = os.RemoveAll(filepath.Join(dir, redisDataDir))
	}
	if s.IsWP() && s.Redis == nil {
		j.Step("connect WordPress")
		if err := m.wpRedis(ctx, s, true); err != nil {
			return fmt.Errorf("Redis runs, but WordPress could not be connected to it: %v", err)
		}
	}
	return nil
}

func (m *Manager) redisOff(ctx context.Context, j *Job, s Site) error {
	dir := m.dir(s.ID)
	next := s
	next.Redis = nil
	if s.IsWP() {
		j.Step("disconnect WordPress")
		if err := m.wpRedis(ctx, s, false); err != nil {
			return fmt.Errorf("WordPress could not be disconnected from Redis, so Redis stays on: %v", err)
		}
	}
	if s.IsRuntime() {
		if err := m.redisAppEnv(s.ID, ""); err != nil {
			return err
		}
	}
	j.Step("stop redis")
	if err := m.redisStack(ctx, s, next); err != nil {
		return err
	}
	for _, f := range []string{filepath.Join("secrets", redisPassFile), filepath.Join("secrets", redisConfFile)} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	_ = os.RemoveAll(filepath.Join(dir, redisDataDir))
	return nil
}

// redisAppEnv sets (pass) or removes ("") the Redis values of a runtime app's environment file.
func (m *Manager) redisAppEnv(id, pass string) error {
	env, err := readEnv(m.envPath(id))
	if err != nil {
		return err
	}
	for _, k := range []string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD"} {
		delete(env, k)
	}
	if pass != "" {
		env["REDIS_URL"] = "redis://:" + pass + "@redis:" + redisPort + "/0"
		env["REDIS_HOST"], env["REDIS_PORT"], env["REDIS_PASSWORD"] = "redis", redisPort, pass
	}
	return m.writeEnv(id, env)
}

// wpRedisPass is the PHP that reads the password in wp-config.php at run time (never the password itself).
const wpRedisPass = `trim((string) @file_get_contents('/run/secrets/` + redisPassFile + `'))`

// wpRedis connects WordPress to its Redis (the plugin, the constants, the object cache) or disconnects it.
func (m *Manager) wpRedis(ctx context.Context, s Site, on bool) error {
	var cmds [][]string
	if on {
		cmds = [][]string{
			{"config", "set", "WP_REDIS_HOST", "redis", "--type=constant"},
			{"config", "set", "WP_REDIS_PORT", redisPort, "--raw", "--type=constant"},
			{"config", "set", "WP_REDIS_PASSWORD", wpRedisPass, "--raw", "--type=constant"},
			{"config", "set", "WP_REDIS_CLIENT", "predis", "--type=constant"},
			{"config", "set", "WP_REDIS_PREFIX", s.ID + ":", "--type=constant"},
			{"plugin", "install", "redis-cache", "--activate"},
			{"redis", "enable", "--force"},
		}
	} else {
		cmds = [][]string{
			{"redis", "disable"},
			{"plugin", "deactivate", "redis-cache"},
		}
		for _, k := range []string{"WP_REDIS_HOST", "WP_REDIS_PORT", "WP_REDIS_PASSWORD", "WP_REDIS_CLIENT", "WP_REDIS_PREFIX"} {
			cmds = append(cmds, []string{"config", "delete", k, "--type=constant"})
		}
	}
	for i := range cmds {
		cmds[i] = append([]string{"wp"}, cmds[i]...)
	}
	res, err := m.wp(ctx, s.ID, nil, cmds...)
	if err != nil {
		return err
	}
	for i, r := range res {
		if r.RC == 0 {
			continue
		}
		t := r.text()
		// turning off is forgiving: a plugin removed by hand or a constant already gone is fine
		if !on && (strings.Contains(t, "not installed") || strings.Contains(t, "not defined") || strings.Contains(t, "not active") ||
			strings.Contains(t, "No object cache") || strings.Contains(t, "is not a registered") || strings.Contains(t, "does not exist")) {
			continue
		}
		return fmt.Errorf("%s: %s", strings.Join(cmds[i], " "), tail([]byte(t)))
	}
	return nil
}
