package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/thuekx/docker-panel-VPS/panel/internal/agent"
)

// containerRow is one container as the site page shows it (STACK-01).
type containerRow struct {
	Service  string `json:"service"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`  // running | exited | ...
	Health   string `json:"health"` // healthy | unhealthy | starting | ""
	Tone     string `json:"tone"`   // ok | warn | bad | unknown: the pill colour, never green unless running and not unhealthy
	CPU      string `json:"cpu"`    // "0.4%" or "—"
	CPUPct   int    `json:"cpu_pct"`
	Mem      string `json:"mem"` // "98 MB / 512 MB" or "—"
	MemPct   int    `json:"mem_pct"`
	Restarts int    `json:"restarts"`
	Since    string `json:"since"` // "2 j 5 m" or "—"
}

type stackView struct {
	Project    string         `json:"project"`
	Containers []containerRow `json:"containers"`
	Up, Total  int
	Err        string `json:"error,omitempty"`
}

func human(b int64) string {
	switch {
	case b < 0:
		return "—"
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%d MB", int64(math.Round(float64(b)/(1<<20))))
	}
}

func since(start int64, now time.Time) string {
	if start <= 0 {
		return "—"
	}
	d := now.Sub(time.Unix(start, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func clampPct(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return int(math.Round(v))
}

func (s *Server) stackOf(st agent.Stack) stackView {
	v := stackView{Project: st.Project, Containers: []containerRow{}}
	now := s.now()
	for _, c := range st.Containers {
		row := containerRow{Service: c.Service, Name: c.Name, Image: c.Image, State: c.State, Health: c.Health,
			CPU: "—", Mem: "—", Restarts: c.Restarts, Since: since(c.Started, now)}
		switch {
		case c.State == "running" && c.Health == "unhealthy":
			row.Tone = "bad"
		case c.State == "running" && c.Health == "starting", c.State == "restarting":
			row.Tone = "warn"
		case c.State == "running":
			row.Tone = "ok"
			v.Up++
		case c.State == "exited", c.State == "dead":
			row.Tone = "bad"
		default:
			row.Tone = "unknown"
		}
		if c.CPU >= 0 && c.State == "running" {
			row.CPU = fmt.Sprintf("%.1f%%", c.CPU)
			row.CPUPct = clampPct(c.CPU)
		}
		if c.MemBytes >= 0 && c.State == "running" {
			row.Mem = human(c.MemBytes)
			if c.MemLimit > 0 {
				row.Mem += " / " + human(c.MemLimit)
				row.MemPct = clampPct(float64(c.MemBytes) * 100 / float64(c.MemLimit))
			}
		}
		v.Containers = append(v.Containers, row)
	}
	v.Total = len(v.Containers)
	return v
}

// siteStack answers the site page's live poll (JSON, no secrets).
func (s *Server) siteStack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !siteRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := s.agentCtx(r, 20*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	st, err := s.ag.Stack(ctx, id)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(stackView{Containers: []containerRow{}, Err: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(s.stackOf(st))
}
