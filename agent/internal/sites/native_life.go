package sites

// Logs, trash, restore and removal of native websites (NATIVE-01). In the trash a native website keeps its Linux
// user and its database (stopped, unreachable: no PHP service, no Nginx server, no edge entry), as a Docker website
// keeps its database folder; deleting it for good drops both.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nativeLogs: web = the website's Nginx error and access log, php = its PHP-FPM service journal and PHP error log.
func (m *Manager) nativeLogs(ctx context.Context, s Site, service string, lines int) (string, error) {
	logs := filepath.Join(m.dir(s.ID), "logs")
	var b strings.Builder
	switch service {
	case "web":
		for _, f := range []string{"error.log", "access.log"} {
			fmt.Fprintf(&b, "==> %s <==\n%s\n", f, tailFile(filepath.Join(logs, f), lines))
		}
	case "php":
		out, err := m.hostRun(ctx, nil, "journalctl", "--no-pager", "--output=short-iso", "--lines", fmt.Sprint(lines), "--unit", NativeUnit(s.ID))
		if err != nil {
			return "", fmt.Errorf("logs failed: %s", tail(out))
		}
		fmt.Fprintf(&b, "==> PHP-FPM <==\n%s\n==> php-error.log <==\n%s\n", out, tailFile(filepath.Join(logs, "php-error.log"), lines))
	default:
		return "", fmt.Errorf("service %q is not part of this site", service)
	}
	return b.String(), nil
}

// tailFile returns the last lines of a log file the website writes (read without following a link).
func tailFile(path string, lines int) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const max = 1 << 20 // the last MB is plenty for 1000 lines
	if fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, 0); err != nil {
			return ""
		}
	}
	buf := make([]byte, max)
	n, _ := f.Read(buf)
	l := strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n")
	if len(l) > lines {
		l = l[len(l)-lines:]
	}
	return strings.Join(l, "\n")
}

// nativeTakeDown stops a website and takes away its PHP service, Nginx server and edge entry (trash, removal).
func (m *Manager) nativeTakeDown(ctx context.Context, s Site) error {
	p := m.npaths
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "disable", "--now", NativeUnit(s.ID)); err != nil && !strings.Contains(string(out), "not loaded") && !strings.Contains(string(out), "does not exist") {
			return fmt.Errorf("stop failed: %s", tail(out))
		}
	}
	for _, f := range []string{p.unit(s.ID), p.fpmConf(s.ID), p.nginx(s.ID), p.edge(s.ID)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_ = os.Remove(filepath.Dir(p.fpmConf(s.ID)))
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("stop failed: %s", tail(out))
		}
	}
	if out, err := m.nginxReload(ctx); err != nil {
		return fmt.Errorf("stop failed: %s", tail(out))
	}
	return nil
}

// nativeBringBack writes a restored website's files again and starts it.
func (m *Manager) nativeBringBack(ctx context.Context, s Site) error {
	if err := m.writeNativeFiles(s); err != nil {
		return err
	}
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("start failed: %s", tail(out))
		}
		if out, err := m.hostRun(ctx, nil, "systemctl", "enable", "--now", NativeUnit(s.ID)); err != nil {
			return fmt.Errorf("start failed: %s", tail(out))
		}
	}
	if out, err := m.nginxReload(ctx); err != nil {
		return fmt.Errorf("start failed: %s", tail(out))
	}
	return nil
}

// nativeDrop removes a website's database, database user and Linux user for good.
func (m *Manager) nativeDrop(ctx context.Context, s Site) error {
	if s.HasDB() {
		db := NativeDB(s.ID)
		if out, err := m.nativeSQL(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%[1]s`;\nDROP USER IF EXISTS '%[1]s'@'localhost';\n", db)); err != nil {
			return fmt.Errorf("database removal failed: %s", tail(out))
		}
	}
	u := NativeUser(s.ID)
	if out, err := m.hostRun(ctx, nil, "gpasswd", "--delete", "www-data", u); err != nil && !strings.Contains(string(out), "not a member") && !strings.Contains(string(out), "does not exist") {
		return fmt.Errorf("user removal failed: %s", tail(out))
	}
	if out, err := m.hostRun(ctx, nil, "userdel", u); err != nil && !strings.Contains(string(out), "does not exist") {
		return fmt.Errorf("user removal failed: %s", tail(out))
	}
	return nil
}

// nativeStack is the Stack view of a native website: its Nginx server (on the shared host Nginx) and its PHP-FPM
// service with the memory and restarts systemd counts.
func (m *Manager) nativeStack(ctx context.Context, s Site) (Stack, error) {
	st := Stack{Project: NativeUnit(s.ID)}
	web := Container{Service: "web", Name: "nginx (host)", Image: "nginx (Ubuntu)", State: "exited", Status: "not served", CPU: -1, MemBytes: -1}
	if _, err := os.Stat(m.npaths.nginx(s.ID)); err == nil {
		web.State, web.Status = "running", "served by the host's Nginx"
	}
	st.Containers = append(st.Containers, web)
	if !s.nativePHP() {
		return st, nil
	}
	out, _ := m.hostRun(ctx, nil, "systemctl", "show", "--timestamp=unix", "--property=ActiveState,SubState,MemoryCurrent,MemoryMax,NRestarts,ExecMainStartTimestamp", NativeUnit(s.ID))
	kv := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			kv[k] = v
		}
	}
	php := Container{Service: "php", Name: NativeUnit(s.ID), Image: "php-fpm" + s.PHP + " (ondrej/php)", State: "exited", CPU: -1, MemBytes: -1}
	if kv["ActiveState"] == "active" {
		php.State = "running"
	}
	php.Status = strings.TrimSpace(kv["ActiveState"] + " (" + kv["SubState"] + ")")
	if n, err := strconv.ParseInt(kv["MemoryCurrent"], 10, 64); err == nil {
		php.MemBytes = n
	}
	if n, err := strconv.ParseInt(kv["MemoryMax"], 10, 64); err == nil {
		php.MemLimit = n
	}
	php.Restarts, _ = strconv.Atoi(kv["NRestarts"])
	if ts := strings.TrimPrefix(kv["ExecMainStartTimestamp"], "@"); ts != "" && php.State == "running" {
		php.Started, _ = strconv.ParseInt(ts, 10, 64)
	}
	st.Containers = append(st.Containers, php)
	return st, nil
}

// nativeLimits writes a website's raised (or standard) limits into its PHP-FPM pool, service and Nginx server and
// restarts its PHP; when it does not come back, the previous files are put back and started again.
func (m *Manager) nativeLimits(ctx context.Context, s Site) error {
	if m.native == nil {
		return errNativeOff
	}
	p := m.npaths
	files := []string{p.fpmConf(s.ID), p.unit(s.ID), p.nginx(s.ID), filepath.Join(m.dir(s.ID), "site.json")}
	prev := map[string][]byte{}
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			prev[f] = b
		}
	}
	apply := func(s Site) error {
		if err := m.writeNativeFiles(s); err != nil {
			return err
		}
		return writeSite(m.dir(s.ID), s)
	}
	restart := func() ([]byte, error) {
		if out, err := m.hostRun(ctx, nil, "systemctl", "daemon-reload"); err != nil {
			return out, err
		}
		if s.nativePHP() {
			if out, err := m.hostRun(ctx, nil, "systemctl", "restart", NativeUnit(s.ID)); err != nil {
				return out, err
			}
		}
		return m.nginxReload(ctx)
	}
	putBack := func() {
		for f, b := range prev {
			_ = writeFile(f, b, 0o644)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := apply(s); err != nil {
		putBack()
		return err
	}
	if out, err := restart(); err != nil {
		putBack()
		if out2, err2 := restart(); err2 != nil {
			return fmt.Errorf("the website did not start with the new limits (%s), nor with the previous ones: %s", tail(out), tail(out2))
		}
		return fmt.Errorf("the website did not start with the new limits, it runs with the previous ones again: %s", tail(out))
	}
	return nil
}

// nativeDBSize is what the website's database takes in the host's MariaDB (data and indexes), 0 when unknown.
func (m *Manager) nativeDBSize(ctx context.Context, s Site) int64 {
	out, err := m.nativeSQL(ctx, fmt.Sprintf("SELECT COALESCE(SUM(data_length+index_length),0) FROM information_schema.tables WHERE table_schema='%s';\n", NativeDB(s.ID)))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// nativeDumpTo writes the website's database as SQL to w (backups: with routines and events).
func (m *Manager) nativeDumpTo(ctx context.Context, s Site, full bool, w io.Writer) ([]byte, error) {
	st, ok := m.host.(HostStreamer)
	if !ok {
		return nil, errors.New("database copy is not available")
	}
	args := []string{"--protocol=socket", "--socket=" + m.native.MariaDBSocket, "--single-transaction", "--quick", "--no-tablespaces", "--default-character-set=utf8mb4", "--add-drop-table"}
	if full {
		args = append(args, "--routines", "--events")
	}
	return st.HostTo(ctx, nil, w, "mariadb-dump", append(args, NativeDB(s.ID))...)
}

// nativeLoad empties the website's database and loads SQL from r into it (restore, import).
func (m *Manager) nativeLoad(ctx context.Context, s Site, r io.Reader) ([]byte, error) {
	db := NativeDB(s.ID)
	if out, err := m.nativeSQL(ctx, "DROP DATABASE IF EXISTS `"+db+"`;\nCREATE DATABASE `"+db+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"); err != nil {
		return out, err
	}
	return m.hostRun(ctx, r, "mariadb", "--protocol=socket", "--socket="+m.native.MariaDBSocket, "--batch", "--default-character-set=utf8mb4", db)
}

// nativeSwitch puts restored files in place of the website's folder: PHP stops for the swap, the files become the
// website user's again (the backup may come from before a move), and PHP starts on the new folder.
func (m *Manager) nativeSwitch(ctx context.Context, s Site, stage string, taken func()) error {
	uid, gid, err := m.lookupUser(NativeUser(s.ID))
	if err != nil {
		return err
	}
	if err := chownTree(stage, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0o750); err != nil {
		return err
	}
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "stop", NativeUnit(s.ID)); err != nil {
			return fmt.Errorf("stop failed: %s", tail(out))
		}
	}
	wp := m.wpDir(s.ID)
	old := filepath.Join(m.dir(s.ID), "wp.old-"+randHex(4))
	if err := os.Rename(wp, old); err != nil {
		return err
	}
	if err := os.Rename(stage, wp); err != nil {
		_ = os.Rename(old, wp)
		return err
	}
	taken()
	if s.nativePHP() {
		if out, err := m.hostRun(ctx, nil, "systemctl", "start", NativeUnit(s.ID)); err != nil {
			return fmt.Errorf("start after restore failed: %s", tail(out))
		}
	}
	return os.RemoveAll(old)
}
