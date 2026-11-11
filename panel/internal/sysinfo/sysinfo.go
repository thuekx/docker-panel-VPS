// Package sysinfo reads host load figures and Docker status. Unknown values stay unknown.
package sysinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Status string

const (
	OK      Status = "ok"
	Warn    Status = "warn"
	Bad     Status = "bad"
	Unknown Status = "unknown"
)

type Snapshot struct {
	Hostname                string
	MemTotalMB, MemUsedMB   int64
	MemStatus               Status
	DiskTotalGB, DiskUsedGB float64
	DiskStatus              Status
	Load1                   float64
	CPUs                    int
	LoadStatus              Status
	UptimeHours             int64
	DockerVersion           string
	ContainersRunning       int
	ContainersTotal         int
	DockerStatus            Status
	BackupLast              time.Time
	BackupStatus            Status
}

type Reader struct {
	ProcDir    string // e.g. /host/proc
	DiskPath   string // path on the data disk
	DockerURL  string // e.g. http://docker-api:2375 (socket proxy)
	BackupFile string // status line written by the backup runner
	Hostname   string
}

func (r Reader) Read(ctx context.Context) Snapshot {
	s := Snapshot{Hostname: r.Hostname, MemStatus: Unknown, DiskStatus: Unknown, LoadStatus: Unknown, DockerStatus: Unknown, BackupStatus: Unknown}
	if mi, err := readKV(r.ProcDir + "/meminfo"); err == nil {
		tot, avail := mi["MemTotal"]/1024, mi["MemAvailable"]/1024
		if tot > 0 {
			s.MemTotalMB, s.MemUsedMB = tot, tot-avail
			s.MemStatus = level(float64(s.MemUsedMB)/float64(tot), 0.80, 0.92)
		}
	}
	var st syscall.Statfs_t
	if r.DiskPath != "" && syscall.Statfs(r.DiskPath, &st) == nil && st.Blocks > 0 {
		tot := float64(st.Blocks) * float64(st.Bsize)
		free := float64(st.Bavail) * float64(st.Bsize)
		s.DiskTotalGB, s.DiskUsedGB = tot/1e9, (tot-free)/1e9
		s.DiskStatus = level((tot-free)/tot, 0.80, 0.90)
	}
	if b, err := os.ReadFile(r.ProcDir + "/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			s.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
		s.CPUs = countCPUs(r.ProcDir + "/cpuinfo")
		if s.CPUs > 0 {
			s.LoadStatus = level(s.Load1/float64(s.CPUs), 0.8, 1.5)
		}
	}
	if b, err := os.ReadFile(r.ProcDir + "/uptime"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			sec, _ := strconv.ParseFloat(f[0], 64)
			s.UptimeHours = int64(sec / 3600)
		}
	}
	r.docker(ctx, &s)
	r.backup(&s)
	return s
}

func level(ratio, warn, bad float64) Status {
	switch {
	case ratio >= bad:
		return Bad
	case ratio >= warn:
		return Warn
	}
	return OK
}

func readKV(path string) (map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fs := strings.Fields(v)
		if len(fs) > 0 {
			n, _ := strconv.ParseInt(fs[0], 10, 64)
			out[k] = n
		}
	}
	return out, sc.Err()
}

func countCPUs(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\nprocessor") + map[bool]int{true: 1, false: 0}[strings.HasPrefix(string(b), "processor")]
}

func (r Reader) docker(ctx context.Context, s *Snapshot) {
	if r.DockerURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cl := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.DockerURL, "/")+"/info", nil)
	resp, err := cl.Do(req)
	if err != nil {
		s.DockerStatus = Bad
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		s.DockerStatus = Bad
		return
	}
	var info struct {
		ServerVersion     string
		Containers        int
		ContainersRunning int
	}
	if json.NewDecoder(resp.Body).Decode(&info) != nil {
		s.DockerStatus = Unknown
		return
	}
	s.DockerVersion, s.ContainersTotal, s.ContainersRunning = info.ServerVersion, info.Containers, info.ContainersRunning
	s.DockerStatus = OK
}

// backup reads "result=PASS unix=<seconds>" written by the nightly backup; older than 36 h is not green.
func (r Reader) backup(s *Snapshot) {
	b, err := os.ReadFile(r.BackupFile)
	if err != nil {
		return
	}
	var res string
	var ts int64
	for _, f := range strings.Fields(string(b)) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "result":
			res = v
		case "unix":
			ts, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if ts == 0 {
		return
	}
	s.BackupLast = time.Unix(ts, 0)
	switch {
	case res != "PASS":
		s.BackupStatus = Bad
	case time.Since(s.BackupLast) > 36*time.Hour:
		s.BackupStatus = Warn
	default:
		s.BackupStatus = OK
	}
}
