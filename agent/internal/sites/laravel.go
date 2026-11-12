package sites

// Laravel websites (APP-01 wave 2): install with Composer at create time, a .env written by the agent, and a short
// fixed list of artisan commands the panel may run. The database password never goes into .env (LaravelPrepend).

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// installLaravel fills the empty website folder with a fresh Laravel project and prepares it: .env, app key,
// package discovery, database tables and the public storage link. Every command runs as www-data in the site's
// own PHP container; no value from outside reaches a command line.
func (m *Manager) installLaravel(ctx context.Context, s Site) ([]byte, error) {
	run := func(args ...string) ([]byte, error) {
		return m.compose(ctx, s.ID, nil, append([]string{"run", "--rm", "-T", "--no-deps", "php"}, args...)...)
	}
	if out, err := run("composer", "create-project", "--no-interaction", "--no-scripts", "--prefer-dist",
		"--no-progress", "laravel/laravel", "."); err != nil {
		return out, err
	}
	if err := m.writeLaravelEnv(s); err != nil {
		return nil, err
	}
	for _, cmd := range [][]string{
		{"php", "artisan", "key:generate", "--force", "--no-interaction"},
		{"php", "artisan", "package:discover", "--no-interaction"},
		{"php", "artisan", "storage:link", "--no-interaction"},
	} {
		if out, err := run(cmd...); err != nil {
			return out, err
		}
	}
	// migrate starts the database first (depends_on: healthy)
	return m.compose(ctx, s.ID, nil, "run", "--rm", "-T", "php", "php", "artisan", "migrate", "--force", "--no-interaction")
}

var envKeyRe = regexp.MustCompile(`^#?\s*([A-Z][A-Z0-9_]*)\s*=`)

// laravelEnvManaged are the .env keys the agent writes; any line for them in .env.example (set or commented out)
// is replaced by the agent's block.
var laravelEnvManaged = map[string]bool{
	"APP_NAME": true, "APP_ENV": true, "APP_DEBUG": true, "APP_URL": true,
	"DB_CONNECTION": true, "DB_HOST": true, "DB_PORT": true, "DB_DATABASE": true, "DB_USERNAME": true, "DB_PASSWORD": true,
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,60}$`)

// laravelEnv turns Laravel's .env.example into the website's .env: production mode, the website address and the
// database settings. DB_PASSWORD is left out on purpose: it comes from the secret file.
func laravelEnv(example []byte, s Site) []byte {
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(example))
	for sc.Scan() {
		line := sc.Text()
		if m := envKeyRe.FindStringSubmatch(line); m != nil && laravelEnvManaged[m[1]] {
			continue
		}
		b.WriteString(line + "\n")
	}
	name := s.Domain
	if plainName.MatchString(s.Title) {
		name = s.Title
	}
	fmt.Fprintf(&b, `
# Written by Nostek Panel (%s).
APP_NAME="%s"
APP_ENV=production
APP_DEBUG=false
APP_URL=%s

# The database is the website's own MariaDB. These values are also set by the panel and win over this file.
# DB_PASSWORD is not here on purpose: the panel hands it to Laravel from a secret file (DB_PASSWORD_FILE).
DB_CONNECTION=mysql
DB_HOST=db
DB_PORT=3306
DB_DATABASE=app
DB_USERNAME=app
`, time.Now().UTC().Format("2006-01-02"), name, s.URL)
	return b.Bytes()
}

func (m *Manager) writeLaravelEnv(s Site) error {
	r, err := os.OpenRoot(m.wpDir(s.ID))
	if err != nil {
		return err
	}
	defer r.Close()
	ex, err := r.ReadFile(".env.example")
	if err != nil {
		return fmt.Errorf("Laravel has no .env.example: %w", err)
	}
	f, err := r.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(laravelEnv(ex, s)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.Chown(".env", m.uid(), m.uid()); err != nil {
		return err
	}
	return r.Chmod(".env", 0o640)
}

// Artisan commands the panel may run on a Laravel website, by name. Nothing else reaches artisan.
var artisanOps = map[string][]string{
	"migrate":        {"migrate", "--force", "--no-interaction"},
	"optimize":       {"optimize", "--no-interaction"},
	"optimize-clear": {"optimize:clear", "--no-interaction"},
	"queue-restart":  {"queue:restart", "--no-interaction"},
}

// Artisan runs one listed artisan command in the website's running PHP container and returns its output.
func (m *Manager) Artisan(ctx context.Context, id, op string) (string, error) {
	args, ok := artisanOps[op]
	if !ok {
		return "", fmt.Errorf("%w: unknown artisan command", ErrBadInput)
	}
	s, err := m.Get(id)
	if err != nil {
		return "", err
	}
	if !s.IsLaravel() {
		return "", fmt.Errorf("%w: this website does not run Laravel", ErrBadInput)
	}
	if s.Phase != "ready" {
		return "", fmt.Errorf("%w: the website is not ready", ErrBadInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := m.compose(ctx, id, nil, append([]string{"exec", "-T", "php", "php", "artisan"}, args...)...)
	text := strings.TrimSpace(string(out))
	if len(text) > 8000 {
		text = "..." + text[len(text)-8000:]
	}
	if err != nil {
		return text, fmt.Errorf("%w: artisan failed: %s", ErrBadInput, tail(out))
	}
	return text, nil
}
