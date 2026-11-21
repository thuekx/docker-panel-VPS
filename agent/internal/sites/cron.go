package sites

// Cron jobs (OPS-01), like cPanel's Cron Jobs: commands a website runs on a schedule (five cron fields, WIB),
// inside the website's own container as its file user (Docker) or as the website's Linux user in its sandbox
// (native). Never on the host as root. A job that still runs when its time comes again is skipped; a run stops
// after an hour. The last runs of each job (exit code, time, the end of the output) are kept in
// <data>/cron/<id>.json.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	cronMax     = 20
	cronCmdMax  = 1000
	cronRuns    = 5
	cronOutMax  = 4 << 10
	cronTimeout = time.Hour
)

// CronJob is one scheduled command of a website.
type CronJob struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"` // five fields or @hourly, @daily, @weekly, @monthly
	Command  string `json:"command"`
	Off      bool   `json:"off,omitempty"`
}

// CronRun is one run of a job.
type CronRun struct {
	Start  int64  `json:"start"`
	Millis int64  `json:"ms"`
	Code   int    `json:"code"` // -1: it could not start or was stopped
	Output string `json:"output"`
	Manual bool   `json:"manual,omitempty"`
}

// cronSpec is a parsed schedule: the allowed values of each field.
type cronSpec struct {
	min, hour, dom, mon, dow [61]bool
	domAny, dowAny           bool
}

var cronAliases = map[string]string{"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *"}

func cronField(f string, lo, hi int, set *[61]bool) error {
	for _, part := range strings.Split(f, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 || n > hi {
				return fmt.Errorf("bad step %q", part)
			}
			step = n
		}
		a, b := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			x, y, _ := strings.Cut(rng, "-")
			var e1, e2 error
			a, e1 = strconv.Atoi(x)
			b, e2 = strconv.Atoi(y)
			if e1 != nil || e2 != nil || a > b {
				return fmt.Errorf("bad range %q", part)
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return fmt.Errorf("bad value %q", part)
			}
			a, b = n, n
			if hasStep {
				b = hi
			}
		}
		if a < lo || b > hi {
			return fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			set[v] = true
		}
	}
	return nil
}

// parseCron reads a schedule.
func parseCron(s string) (cronSpec, error) {
	var c cronSpec
	s = strings.TrimSpace(s)
	if a, ok := cronAliases[strings.ToLower(s)]; ok {
		s = a
	}
	f := strings.Fields(s)
	if len(f) != 5 {
		return c, errors.New("a schedule has five fields: minute hour day month weekday")
	}
	for i, x := range []struct {
		lo, hi int
		set    *[61]bool
	}{{0, 59, &c.min}, {0, 23, &c.hour}, {1, 31, &c.dom}, {1, 12, &c.mon}, {0, 7, &c.dow}} {
		if err := cronField(f[i], x.lo, x.hi, x.set); err != nil {
			return c, err
		}
	}
	if c.dow[7] {
		c.dow[0] = true
	}
	c.domAny, c.dowAny = f[2] == "*", f[4] == "*"
	return c, nil
}

// due is true when the schedule runs at minute t (as cron does: day of month OR weekday when both are set).
func (c cronSpec) due(t time.Time) bool {
	if !c.min[t.Minute()] || !c.hour[t.Hour()] || !c.mon[int(t.Month())] {
		return false
	}
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}

// next is the next minute after t the schedule runs (within a year), or zero.
func (c cronSpec) next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		if c.due(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// cronSupported is false for websites with no shell to run a command in (static files only).
func cronSupported(s Site) bool { return s.AppName() != "static" }

// cronService is the container a Docker website's jobs run in, and the user they run as ("" = the container's).
func cronService(s Site) (string, string) {
	switch {
	case s.OneApp():
		return "app", ""
	case s.Server == "apache":
		return "web", "33:33"
	}
	return "php", "33:33"
}

type cronState struct {
	mu      sync.Mutex
	runs    map[string]map[string][]CronRun // site -> job -> newest last
	running map[string]bool                 // site/job
	loaded  map[string]bool
}

func (m *Manager) cronFile(id string) string { return filepath.Join(m.cfg.Data, "cron", id+".json") }

func (m *Manager) cronDB() *cronState {
	m.cronOnce.Do(func() {
		m.crons = &cronState{runs: map[string]map[string][]CronRun{}, running: map[string]bool{}, loaded: map[string]bool{}}
	})
	return m.crons
}

// cronRunsOf loads a website's runs once (cs.mu held).
func (m *Manager) cronRunsOf(cs *cronState, id string) map[string][]CronRun {
	if !cs.loaded[id] {
		cs.loaded[id] = true
		r := map[string][]CronRun{}
		if b, err := os.ReadFile(m.cronFile(id)); err == nil {
			_ = json.Unmarshal(b, &r)
		}
		cs.runs[id] = r
	}
	return cs.runs[id]
}

func (m *Manager) saveCronRuns(cs *cronState, id string) {
	b, err := json.Marshal(cs.runs[id])
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(m.cronFile(id)), 0o700) == nil {
		_ = writeFile(m.cronFile(id), b, 0o600)
	}
}

// cronOutput keeps the end of an output, as valid text.
func cronOutput(b []byte) string {
	if len(b) > cronOutMax {
		b = b[len(b)-cronOutMax:]
		for len(b) > 0 && !utf8.Valid(b[:min(len(b), 4)]) {
			b = b[1:]
		}
	}
	return strings.ToValidUTF8(string(b), "?")
}

// runCron runs one job now and records the run; false when it still runs from before.
func (m *Manager) runCron(ctx context.Context, s Site, j CronJob, manual bool) bool {
	cs := m.cronDB()
	key := s.ID + "/" + j.ID
	cs.mu.Lock()
	if cs.running[key] {
		cs.mu.Unlock()
		return false
	}
	cs.running[key] = true
	cs.mu.Unlock()
	start := m.now()
	ctx, cancel := context.WithTimeout(ctx, cronTimeout)
	defer cancel()
	var out []byte
	var err error
	if s.IsNative() {
		if m.native == nil {
			err = errNativeOff
		} else {
			a := []string{"--quiet", "--pipe", "--wait", "--collect", "--expand-environment=no", "--uid=" + NativeUser(s.ID), "--gid=" + NativeUser(s.ID),
				"--working-directory=" + m.npaths.docroot(s.ID)}
			for _, p := range s.nativeSandbox(m.npaths) {
				a = append(a, "-p", p)
			}
			out, err = m.hostRun(ctx, nil, "systemd-run", append(a, "--", "/bin/sh", "-c", j.Command)...)
		}
	} else {
		svc, user := cronService(s)
		args := []string{"exec", "-T"}
		if user != "" {
			args = append(args, "-u", user)
		}
		out, err = m.compose(ctx, s.ID, nil, append(args, svc, "sh", "-c", j.Command)...)
	}
	r := CronRun{Start: start.Unix(), Millis: m.now().Sub(start).Milliseconds(), Output: cronOutput(out), Manual: manual}
	var ee interface{ ExitCode() int }
	switch {
	case err == nil:
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		r.Code = ee.ExitCode()
	default:
		r.Code = -1
		if ctx.Err() != nil {
			r.Output = cronOutput(append(out, []byte("\n[stopped after one hour]")...))
		} else if len(bytes.TrimSpace(out)) == 0 {
			r.Output = err.Error()
		}
	}
	cs.mu.Lock()
	runs := m.cronRunsOf(cs, s.ID)
	l := append(runs[j.ID], r)
	if len(l) > cronRuns {
		l = l[len(l)-cronRuns:]
	}
	runs[j.ID] = l
	m.saveCronRuns(cs, s.ID)
	delete(cs.running, key)
	cs.mu.Unlock()
	return true
}

// CronTick starts every job due at minute t (WIB) and returns how many started.
func (m *Manager) CronTick(ctx context.Context, t time.Time, wg *sync.WaitGroup) int {
	list, err := m.List()
	if err != nil {
		return 0
	}
	t = t.In(wib)
	n := 0
	for _, s := range list {
		if s.Phase != "ready" || !cronSupported(s) {
			continue
		}
		for _, j := range s.Crons {
			c, err := parseCron(j.Schedule)
			if j.Off || err != nil || !c.due(t) {
				continue
			}
			n++
			wg.Add(1)
			go func(s Site, j CronJob) {
				defer wg.Done()
				m.runCron(ctx, s, j, false)
			}(s, j)
		}
	}
	return n
}

// UserCronLoop runs the cron jobs at the start of every minute until ctx ends; running jobs get their hour.
func (m *Manager) UserCronLoop(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		now := m.now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		m.CronTick(context.WithoutCancel(ctx), next, &wg)
	}
}

// CronJobView is one job on the Cron page.
type CronJobView struct {
	CronJob
	Next    int64     `json:"next"`
	Running bool      `json:"running"`
	Runs    []CronRun `json:"runs"` // newest first
}

// CronView is the Cron page of a website.
type CronView struct {
	Supported bool          `json:"supported"`
	Where     string        `json:"where"` // the container (Docker) or "native"
	Jobs      []CronJobView `json:"jobs"`
	Max       int           `json:"max"`
}

// Cron returns a website's jobs with their next time and last runs.
func (m *Manager) Cron(id string) (CronView, error) {
	s, err := m.Get(id)
	if err != nil {
		return CronView{}, err
	}
	v := CronView{Supported: cronSupported(s), Jobs: []CronJobView{}, Max: cronMax, Where: "native"}
	if !s.IsNative() {
		v.Where, _ = cronService(s)
	}
	cs := m.cronDB()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	runs := m.cronRunsOf(cs, id)
	for _, j := range s.Crons {
		jv := CronJobView{CronJob: j, Running: cs.running[id+"/"+j.ID], Runs: []CronRun{}}
		if c, err := parseCron(j.Schedule); err == nil && !j.Off {
			if t := c.next(m.now().In(wib)); !t.IsZero() {
				jv.Next = t.Unix()
			}
		}
		l := runs[j.ID]
		for i := len(l) - 1; i >= 0; i-- {
			jv.Runs = append(jv.Runs, l[i])
		}
		v.Jobs = append(v.Jobs, jv)
	}
	return v, nil
}

// CronChange is one change on the Cron page: op add | edit | remove | on | off | run.
type CronChange struct {
	Op       string `json:"op"`
	ID       string `json:"id,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	Command  string `json:"command,omitempty"`
}

func checkCronCommand(c string) (string, error) {
	c = strings.TrimSpace(c)
	switch {
	case c == "":
		return "", fmt.Errorf("%w: the command is empty", ErrBadInput)
	case len(c) > cronCmdMax:
		return "", fmt.Errorf("%w: the command is longer than %d characters", ErrBadInput, cronCmdMax)
	case strings.ContainsAny(c, "\x00\r\n") || !utf8.ValidString(c):
		return "", fmt.Errorf("%w: the command must be one line", ErrBadInput)
	}
	return c, nil
}

// ChangeCron applies one change; "run" starts the job now in the background. It returns the job's id.
func (m *Manager) ChangeCron(id string, c CronChange) (string, error) {
	s, err := m.readySite(id)
	if err != nil {
		return "", err
	}
	if !cronSupported(s) {
		return "", fmt.Errorf("%w: a static website has no container to run commands in", ErrBadInput)
	}
	find := func() int {
		return slices.IndexFunc(s.Crons, func(j CronJob) bool { return j.ID == c.ID })
	}
	if c.Op == "run" {
		i := find()
		if i < 0 {
			return "", fmt.Errorf("%w: no such job", ErrBadInput)
		}
		cs := m.cronDB()
		cs.mu.Lock()
		busy := cs.running[id+"/"+c.ID]
		cs.mu.Unlock()
		if busy {
			return "", fmt.Errorf("%w: the job is still running", ErrBusy)
		}
		go m.runCron(context.Background(), s, s.Crons[i], true)
		return c.ID, nil
	}
	if !m.mu.TryLock() {
		return "", ErrBusy
	}
	defer m.mu.Unlock()
	next := append([]CronJob(nil), s.Crons...)
	switch c.Op {
	case "add", "edit":
		sched := strings.Join(strings.Fields(c.Schedule), " ")
		if _, err := parseCron(sched); err != nil {
			return "", fmt.Errorf("%w: schedule %q: %v", ErrBadInput, c.Schedule, err)
		}
		cmd, err := checkCronCommand(c.Command)
		if err != nil {
			return "", err
		}
		if c.Op == "add" {
			if len(next) >= cronMax {
				return "", fmt.Errorf("%w: at most %d jobs per website", ErrBadInput, cronMax)
			}
			b := make([]byte, 3)
			_, _ = rand.Read(b)
			c.ID = hex.EncodeToString(b)
			next = append(next, CronJob{ID: c.ID, Schedule: sched, Command: cmd})
			break
		}
		i := find()
		if i < 0 {
			return "", fmt.Errorf("%w: no such job", ErrBadInput)
		}
		if next[i].Schedule == sched && next[i].Command == cmd {
			return c.ID, ErrNoChange
		}
		next[i].Schedule, next[i].Command = sched, cmd
	case "remove", "on", "off":
		i := find()
		if i < 0 {
			return "", fmt.Errorf("%w: no such job", ErrBadInput)
		}
		switch c.Op {
		case "remove":
			next = slices.Delete(next, i, i+1)
		case "on", "off":
			if next[i].Off == (c.Op == "off") {
				return c.ID, ErrNoChange
			}
			next[i].Off = c.Op == "off"
		}
	default:
		return "", fmt.Errorf("%w: unknown change %q", ErrBadInput, c.Op)
	}
	if len(next) == 0 {
		next = nil
	}
	s.Crons = next
	if err := writeSite(m.dir(id), s); err != nil {
		return "", err
	}
	if c.Op == "remove" {
		cs := m.cronDB()
		cs.mu.Lock()
		delete(m.cronRunsOf(cs, id), c.ID)
		m.saveCronRuns(cs, id)
		cs.mu.Unlock()
	}
	return c.ID, nil
}
