package sites

// Resource usage over time (OPS-01 wave 2). Every five minutes the agent samples the CPU and memory of every
// website (all its containers added up; a native website: its PHP service) and of the server, and keeps the last
// 24 hours at that step and 31 days as hourly points (CPU averaged, memory at its highest).

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	usageEvery  = 5           // minutes between samples
	usageRecent = 24 * 60 / 5 // 5-minute points kept (24 h)
	usageHours  = 31 * 24     // hourly points kept (31 days)
)

// procDir is where the server's figures are read (tests point it elsewhere).
var procDir = "/proc"

// UsagePoint is one sample: CPU in percent of one CPU, memory in bytes. Disk and Load only for the server.
type UsagePoint struct {
	At   int64   `json:"at"`
	CPU  float64 `json:"cpu"`
	Mem  int64   `json:"mem"`
	Disk float64 `json:"disk,omitempty"` // percent of the data disk in use
	Load float64 `json:"load,omitempty"`
	n    int
}

// UsageSeries is what the pages show.
type UsageSeries struct {
	Recent []UsagePoint `json:"recent"` // every 5 minutes, 24 h, oldest first
	Hours  []UsagePoint `json:"hours"`  // hourly, 31 days, oldest first
	CPUs   int          `json:"cpus"`
	MemMax int64        `json:"mem_max"` // the website's memory limit, or the server's memory
}

type usageFile struct {
	Recent []UsagePoint `json:"recent"`
	Hours  []UsagePoint `json:"hours"`
	HourN  int          `json:"hour_n"` // samples in the last hourly point
}

type usageState struct {
	mu   sync.Mutex
	cpu  [2]uint64 // last /proc/stat busy and total
	unit map[string]uint64
	unAt map[string]time.Time
}

func (m *Manager) usagePath(id string) string {
	if id == "" {
		id = "_server"
	}
	return filepath.Join(m.cfg.Data, "usage", id+".json")
}

func (m *Manager) usageSt() *usageState {
	m.usageOnce.Do(func() { m.usage = &usageState{unit: map[string]uint64{}, unAt: map[string]time.Time{}} })
	return m.usage
}

func readUsage(path string) usageFile {
	var f usageFile
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

// addUsage appends a sample and folds it into the hourly point of its hour.
func (m *Manager) addUsage(id string, p UsagePoint) {
	path := m.usagePath(id)
	f := readUsage(path)
	f.Recent = append(f.Recent, p)
	if n := len(f.Recent); n > usageRecent {
		f.Recent = append([]UsagePoint(nil), f.Recent[n-usageRecent:]...)
	}
	hour := time.Unix(p.At, 0).Truncate(time.Hour).Unix()
	if n := len(f.Hours); n > 0 && f.Hours[n-1].At == hour {
		h := &f.Hours[n-1]
		k := float64(f.HourN)
		h.CPU = (h.CPU*k + p.CPU) / (k + 1)
		h.Load = (h.Load*k + p.Load) / (k + 1)
		if p.Mem > h.Mem {
			h.Mem = p.Mem
		}
		if p.Disk > h.Disk {
			h.Disk = p.Disk
		}
		f.HourN++
	} else {
		hp := p
		hp.At = hour
		f.Hours = append(f.Hours, hp)
		f.HourN = 1
		if n := len(f.Hours); n > usageHours {
			f.Hours = append([]UsagePoint(nil), f.Hours[n-usageHours:]...)
		}
	}
	if b, err := json.Marshal(f); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		_ = writeFile(path, b, 0o600)
	}
}

// dropUsage removes a website's usage file (with its statistics, when the website is deleted for good).
func (m *Manager) dropUsage(id string) {
	if id != "" {
		_ = os.Remove(m.usagePath(id))
	}
}

// Usage returns a website's usage, or the server's for id "".
func (m *Manager) Usage(id string) (UsageSeries, error) {
	v := UsageSeries{CPUs: countCPU()}
	if id != "" {
		s, err := m.Get(id)
		if err != nil {
			return v, err
		}
		v.MemMax = int64(s.memLimitMB()) << 20
	} else {
		mi := meminfo()
		v.MemMax = mi["MemTotal"] << 10
	}
	f := readUsage(m.usagePath(id))
	v.Recent, v.Hours = f.Recent, f.Hours
	if v.Recent == nil {
		v.Recent = []UsagePoint{}
	}
	if v.Hours == nil {
		v.Hours = []UsagePoint{}
	}
	return v, nil
}

// UsageTick takes one sample of every website and of the server.
func (m *Manager) UsageTick(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	u := m.usageSt()
	u.mu.Lock()
	defer u.mu.Unlock()
	at := now.Unix()
	m.addUsage("", m.serverSample(u, at))
	list, _ := m.List()
	// one docker stats call over every running container; names are <project>-<service>-<n>
	cpu, mem := map[string]float64{}, map[string]int64{}
	if raw, err := m.run.Docker(ctx, m.cfg.Data, nil, "stats", "--no-stream", "--format", "json"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var x struct{ Name, CPUPerc, MemUsage string }
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &x) != nil {
				continue
			}
			for _, s := range list {
				if s.IsNative() || !strings.HasPrefix(x.Name, Project(s.ID)+"-") {
					continue
				}
				if v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(x.CPUPerc), "%"), 64); err == nil {
					cpu[s.ID] += v
				}
				if used, _, ok := strings.Cut(x.MemUsage, "/"); ok {
					mem[s.ID] += parseSize(used)
				}
			}
		}
	}
	for _, s := range list {
		if s.IsNative() {
			if m.native == nil || !s.nativePHP() {
				continue
			}
			o, _ := m.hostRun(ctx, nil, "systemctl", "show", "--property=MemoryCurrent,CPUUsageNSec", NativeUnit(s.ID))
			kv := map[string]string{}
			for _, l := range strings.Split(string(o), "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
					kv[k] = v
				}
			}
			p := UsagePoint{At: at}
			p.Mem, _ = strconv.ParseInt(kv["MemoryCurrent"], 10, 64)
			if ns, err := strconv.ParseUint(kv["CPUUsageNSec"], 10, 64); err == nil {
				if last, ok := u.unit[s.ID]; ok && ns >= last {
					if dt := now.Sub(u.unAt[s.ID]).Seconds(); dt > 0 {
						p.CPU = float64(ns-last) / 1e9 / dt * 100
					}
				}
				u.unit[s.ID], u.unAt[s.ID] = ns, now
			}
			m.addUsage(s.ID, p)
			continue
		}
		if _, ok := mem[s.ID]; !ok {
			continue // no running container: no point rather than a false zero
		}
		m.addUsage(s.ID, UsagePoint{At: at, CPU: cpu[s.ID], Mem: mem[s.ID]})
	}
}

func (m *Manager) serverSample(u *usageState, at int64) UsagePoint {
	p := UsagePoint{At: at}
	if busy, total, ok := procCPU(); ok {
		if u.cpu[1] > 0 && total > u.cpu[1] {
			p.CPU = float64(busy-u.cpu[0]) / float64(total-u.cpu[1]) * 100 * float64(countCPU())
		}
		u.cpu = [2]uint64{busy, total}
	}
	mi := meminfo()
	p.Mem = (mi["MemTotal"] - mi["MemAvailable"]) << 10
	if b, err := os.ReadFile(filepath.Join(procDir, "loadavg")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			p.Load, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	var st syscall.Statfs_t
	if syscall.Statfs(m.cfg.Data, &st) == nil && st.Blocks > 0 {
		p.Disk = float64(st.Blocks-st.Bavail) / float64(st.Blocks) * 100
	}
	return p
}

// procCPU reads the busy and total jiffies of all CPUs.
func procCPU() (busy, total uint64, ok bool) {
	f, err := os.Open(filepath.Join(procDir, "stat"))
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fs := strings.Fields(sc.Text())
	if len(fs) < 5 || fs[0] != "cpu" {
		return 0, 0, false
	}
	for i, x := range fs[1:] {
		v, _ := strconv.ParseUint(x, 10, 64)
		total += v
		if i != 3 && i != 4 { // idle and iowait
			busy += v
		}
	}
	return busy, total, true
}

func meminfo() map[string]int64 {
	out := map[string]int64{}
	f, err := os.Open(filepath.Join(procDir, "meminfo"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(strings.Fields(v + " 0")[0], 10, 64); err == nil {
			out[k] = n
		}
	}
	return out
}

func countCPU() int {
	b, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return 1
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "cpu") && len(l) > 3 && l[3] >= '0' && l[3] <= '9' {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// memLimitMB is the sum of the memory limits of a website's services (0 when unknown).
func (s Site) memLimitMB() int {
	rams, _, err := s.limitsOf()
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rams {
		n += r.Current
	}
	return n
}
