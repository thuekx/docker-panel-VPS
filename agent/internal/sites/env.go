package sites

// Environment values of a Node.js app (APP-02b). They live in deploy/app.env (0600, root), outside the website
// folder, so the File Manager and the backups of the website folder never show them. The app container reads
// them at start (compose env_file, raw format: no quotes or $ expansion) and the build container gets them too.
// The agent never returns a value: the panel only sees the names.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Env limits.
const (
	MaxEnvKeys  = 100
	MaxEnvValue = 4096
)

var appEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// reservedEnv are set by the panel itself and cannot be overridden.
var reservedEnv = map[string]bool{"PORT": true, "HOST": true, "HOSTNAME": true, "HOME": true, "PATH": true}

// EnvChange sets and deletes environment values of an app.
type EnvChange struct {
	Set    map[string]string `json:"set"`
	Delete []string          `json:"delete"`
}

func (m *Manager) envPath(id string) string { return filepath.Join(m.deployDir(id), "app.env") }

func readEnv(p string) (map[string]string, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && appEnvKeyRe.MatchString(k) {
			env[k] = v
		}
	}
	return env, sc.Err()
}

// EnvNames returns the names of the app's environment values, sorted. Values are never returned.
func (m *Manager) EnvNames(id string) ([]string, error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if !s.IsRuntime() {
		return nil, ErrNotNode
	}
	env, err := readEnv(m.envPath(id))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// SetEnv changes the app's environment values and recreates the app container so it reads them. Values the
// build needs (for example NEXT_PUBLIC_*) take effect at the next deploy.
func (m *Manager) SetEnv(ctx context.Context, id string, c EnvChange) error {
	for k, v := range c.Set {
		if !appEnvKeyRe.MatchString(k) {
			return fmt.Errorf("%w: name %q: letters, digits and _, not starting with a digit, at most 64", ErrBadInput, k)
		}
		if reservedEnv[k] {
			return fmt.Errorf("%w: %s is set by the panel", ErrBadInput, k)
		}
		if len(v) > MaxEnvValue || strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("%w: value of %s must be one line of at most %d characters", ErrBadInput, k, MaxEnvValue)
		}
	}
	for _, k := range c.Delete {
		if !appEnvKeyRe.MatchString(k) {
			return fmt.Errorf("%w: name %q is not valid", ErrBadInput, k)
		}
	}
	if !m.mu.TryLock() {
		return ErrBusy
	}
	defer m.mu.Unlock()
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	if !s.IsRuntime() {
		return ErrNotNode
	}
	env, err := readEnv(m.envPath(id))
	if err != nil {
		return err
	}
	for _, k := range c.Delete {
		delete(env, k)
	}
	for k, v := range c.Set {
		env[k] = v
	}
	if len(env) > MaxEnvKeys {
		return fmt.Errorf("%w: at most %d values", ErrBadInput, MaxEnvKeys)
	}
	if err := m.writeEnv(id, env); err != nil {
		return err
	}
	if s.Release < 1 {
		return nil
	}
	if out, err := m.compose(ctx, id, nil, "up", "-d", "--wait", "--wait-timeout", fmt.Sprint(StartTimeout), "app"); err != nil {
		return fmt.Errorf("values saved, but the app did not start again: %v %s", err, tail(out))
	}
	return nil
}

// writeEnv writes the app's environment file (raw KEY=VALUE lines, sorted, 0600, outside the website folder).
func (m *Manager) writeEnv(id string, env map[string]string) error {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k + "=" + env[k] + "\n")
	}
	if err := os.MkdirAll(m.deployDir(id), 0o755); err != nil {
		return err
	}
	return writeFile(m.envPath(id), []byte(b.String()), 0o600)
}
