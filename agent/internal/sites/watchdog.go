package sites

// Service watchdog (OPS-01 wave 2). Docker restarts a container that stops, but not one that keeps running while
// its health check fails, and nothing restarts a native website's PHP or the host's Nginx or MariaDB once systemd
// has given up. Once a minute the agent checks every website's containers (or native services), the core services
// (edge, panel, WAF, SFTP) and, with the native engine, the host's Nginx and MariaDB. A service that is unhealthy
// or not running on three checks in a row is restarted, at most three times an hour; after that it is left alone
// and reported once. Every restart, failure and recovery is an event the panel reads for its alerts.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	watchBadChecks   = 3   // checks in a row before a restart
	watchMaxRestarts = 3   // restarts per service per hour
	watchKeepEvents  = 200 // events kept in watchdog.json
	// CoreProject is the compose project of the edge, the panel, the WAF and SFTP.
	CoreProject = "nostek-core"
)

// coreServices are the core containers the watchdog restarts (the socket proxy has no health check of its own).
var coreServices = map[string]bool{"edge": true, "panel": true, "waf": true, "sftp": true}

// WatchEvent is one thing the watchdog did or saw. Site is "" for the core services and the host.
type WatchEvent struct {
	Seq     int64  `json:"seq"`
	At      int64  `json:"at"`
	Site    string `json:"site,omitempty"`
	Service string `json:"service"` // web, php, db, app … | edge, panel, waf, sftp | nginx, mariadb
	Kind    string `json:"kind"`    // restarted | restart_failed | gave_up | recovered
	Reason  string `json:"reason"`  // unhealthy | not running
}

// WatchView is what the panel reads: the events after a sequence number.
type WatchView struct {
	Seq    int64        `json:"seq"`
	Events []WatchEvent `json:"events"`
}

type watchFile struct {
	Seq    int64        `json:"seq"`
	Events []WatchEvent `json:"events"`
}

type watchState struct {
	mu       sync.Mutex
	file     watchFile
	bad      map[string]int
	restarts map[string][]int64
	acted    map[string]bool // restarted or gave up: a later healthy check is a recovery
	gaveUp   map[string]bool
}

func (m *Manager) watchPath() string { return filepath.Join(m.cfg.Data, "watchdog.json") }

func (m *Manager) watch() *watchState {
	m.watchOnce.Do(func() {
		w := &watchState{bad: map[string]int{}, restarts: map[string][]int64{}, acted: map[string]bool{}, gaveUp: map[string]bool{}}
		if b, err := os.ReadFile(m.watchPath()); err == nil {
			_ = json.Unmarshal(b, &w.file)
		}
		m.watchSt = w
	})
	return m.watchSt
}

// Watchdog returns the events after since (all kept events for since 0).
func (m *Manager) Watchdog(since int64) WatchView {
	w := m.watch()
	w.mu.Lock()
	defer w.mu.Unlock()
	v := WatchView{Seq: w.file.Seq, Events: []WatchEvent{}}
	for _, e := range w.file.Events {
		if e.Seq > since {
			v.Events = append(v.Events, e)
		}
	}
	return v
}

// watchTarget is one service the watchdog looks after.
type watchTarget struct {
	site, svc string
	ok        bool
	reason    string
	restart   func(ctx context.Context) error
}

func (t watchTarget) key() string { return t.site + "/" + t.svc }

func (m *Manager) addWatchEvent(w *watchState, t watchTarget, kind string, at time.Time) {
	w.file.Seq++
	w.file.Events = append(w.file.Events, WatchEvent{Seq: w.file.Seq, At: at.Unix(), Site: t.site, Service: t.svc, Kind: kind, Reason: t.reason})
	if n := len(w.file.Events); n > watchKeepEvents {
		w.file.Events = append([]WatchEvent(nil), w.file.Events[n-watchKeepEvents:]...)
	}
	if b, err := json.Marshal(w.file); err == nil {
		_ = writeFile(m.watchPath(), b, 0o600)
	}
}

// psProblem says why a container row is not well: "" when it runs and is not unhealthy.
func psProblem(state, health string) string {
	switch {
	case state != "running":
		return "not running"
	case health == "unhealthy":
		return "unhealthy"
	}
	return ""
}

// watchTargets collects every service to check now. A website whose change lock is held (a job is recreating its
// containers) is left out of this round.
func (m *Manager) watchTargets(ctx context.Context) []watchTarget {
	var out []watchTarget
	list, _ := m.List()
	if m.mu.TryLock() {
		m.mu.Unlock()
	} else {
		list = nil
	}
	for _, s := range list {
		s := s
		if s.IsNative() {
			if m.native == nil || !s.nativePHP() {
				continue
			}
			o, _ := m.hostRun(ctx, nil, "systemctl", "is-active", NativeUnit(s.ID))
			ok := strings.TrimSpace(string(o)) == "active"
			out = append(out, watchTarget{site: s.ID, svc: "php", ok: ok, reason: "not running",
				restart: func(ctx context.Context) error { return m.RestartService(ctx, s.ID, "php") }})
			continue
		}
		raw, err := m.compose(ctx, s.ID, nil, "ps", "--all", "--format", "json")
		if err != nil {
			continue
		}
		rows, err := parsePS(raw)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, r := range rows {
			if !validService(s, r.Service) || seen[r.Service] {
				continue
			}
			seen[r.Service] = true
			svc, why := r.Service, psProblem(r.State, r.Health)
			out = append(out, watchTarget{site: s.ID, svc: svc, ok: why == "", reason: why,
				restart: func(ctx context.Context) error { return m.RestartService(ctx, s.ID, svc) }})
		}
	}
	// core services: one `docker ps` over the core project
	if raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "ps", "--all", "--filter", "label=com.docker.compose.project="+CoreProject, "--format", "json"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var r struct {
				Names, State, Status, Labels string
			}
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &r) != nil {
				continue
			}
			svc := labelValue(r.Labels, "com.docker.compose.service")
			if !coreServices[svc] || !containerName.MatchString(r.Names) {
				continue
			}
			health := ""
			if strings.Contains(r.Status, "(unhealthy)") {
				health = "unhealthy"
			}
			name, why := r.Names, psProblem(r.State, health)
			out = append(out, watchTarget{svc: svc, ok: why == "", reason: why, restart: func(ctx context.Context) error {
				if o, err := m.run.Docker(ctx, m.cfg.Data, nil, "restart", name); err != nil {
					return fmt.Errorf("restart %s failed: %s", name, tail(o))
				}
				return nil
			}})
		}
	}
	if m.native != nil {
		units := []string{"nginx"}
		if m.native.MariaDBSocket != "" {
			units = append(units, "mariadb")
		}
		for _, u := range units {
			u := u
			o, _ := m.hostRun(ctx, nil, "systemctl", "is-active", u)
			ok := strings.TrimSpace(string(o)) == "active"
			out = append(out, watchTarget{svc: u, ok: ok, reason: "not running", restart: func(ctx context.Context) error {
				if o, err := m.hostRun(ctx, nil, "systemctl", "restart", u); err != nil {
					return fmt.Errorf("restart %s failed: %s", u, tail(o))
				}
				return nil
			}})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// labelValue reads one label from docker ps's "k=v,k=v" Labels field.
func labelValue(labels, key string) string {
	for _, kv := range strings.Split(labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// WatchTick checks every service once and restarts the ones that need it.
func (m *Manager) WatchTick(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	targets := m.watchTargets(ctx)
	w := m.watch()
	for _, t := range targets {
		k := t.key()
		w.mu.Lock()
		if t.ok {
			if w.acted[k] {
				m.addWatchEvent(w, t, "recovered", now)
			}
			delete(w.bad, k)
			delete(w.acted, k)
			delete(w.gaveUp, k)
			w.mu.Unlock()
			continue
		}
		w.bad[k]++
		if w.bad[k] < watchBadChecks {
			w.mu.Unlock()
			continue
		}
		var recent []int64
		for _, at := range w.restarts[k] {
			if now.Unix()-at < 3600 {
				recent = append(recent, at)
			}
		}
		w.restarts[k] = recent
		if len(recent) >= watchMaxRestarts {
			if !w.gaveUp[k] {
				w.gaveUp[k], w.acted[k] = true, true
				m.addWatchEvent(w, t, "gave_up", now)
			}
			w.mu.Unlock()
			continue
		}
		w.mu.Unlock()
		err := t.restart(ctx)
		if err == ErrBusy { // a change started meanwhile: look again next minute
			continue
		}
		w.mu.Lock()
		w.bad[k] = 0
		w.restarts[k] = append(w.restarts[k], now.Unix())
		w.acted[k] = true
		if err != nil {
			m.addWatchEvent(w, t, "restart_failed", now)
		} else {
			m.addWatchEvent(w, t, "restarted", now)
		}
		w.mu.Unlock()
	}
}

// WatchLoop runs the watchdog and the usage sampler at the start of every minute.
func (m *Manager) WatchLoop(ctx context.Context) {
	for {
		now := m.now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		m.WatchTick(ctx, next)
		if next.Minute()%usageEvery == 0 {
			m.UsageTick(ctx, next)
		}
	}
}
